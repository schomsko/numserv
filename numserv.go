package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	URL         string        = `^((ftp|https?):\/\/)?(\S+(:\S*)?@)?((([1-9]\d?|1\d\d|2[01]\d|22[0-3])(\.(1?\d{1,2}|2[0-4]\d|25[0-5])){2}(?:\.([0-9]\d?|1\d\d|2[0-4]\d|25[0-4]))|(([a-zA-Z0-9]([a-zA-Z0-9-]+)?[a-zA-Z0-9]([-\.][a-zA-Z0-9]+)*)|((www\.)?))?(([a-zA-Z\x{00a1}-\x{ffff}0-9]+-?-?)*[a-zA-Z\x{00a1}-\x{ffff}0-9]+)(?:\.([a-zA-Z\x{00a1}-\x{ffff}]{1,}))?))(:(\d{1,5}))?((\/|\?|#)[^\s]*)?$`
	REQTIMEOUT  time.Duration = 400 // Milliseconds to fetch from servers
	RESPTIMEOUT time.Duration = 450 // Milliseconds to beginn response
)

const (
	FREE    = iota // place can be used for insertion of an element
	READY   = iota // element in place is waiting to be merged
	MERGING = iota // element in place is currently merging
)

var (
	rxURL = regexp.MustCompile(URL)
	mu    = &sync.Mutex{}
)

// StateNums hold numbers received from a server or already merged results.
// The state can be one of the constants: FREE (initially), READY or MERGING.
type StateNums struct {
	state   int
	Numbers []int `json:"numbers"`
}

// Container holding the Statenums will be passed arround by the merging goroutines.
type Container []StateNums

// main ...
func main() {
	http.HandleFunc("/numbers", func(w http.ResponseWriter, r *http.Request) {
		var container Container
		// start := time.Now()
		timeout := make(chan bool, 1)
		finished := make(chan bool, 1)
		count := make(chan bool, 1)
		numOfURLs := len(r.URL.Query()["u"])

		if numOfURLs != 0 {
			container = make(Container, numOfURLs)
			container[0].Numbers = make([]int, 0)

			go func() {
				time.Sleep(RESPTIMEOUT * time.Millisecond)
				timeout <- true
			}()
			go counter(count, finished, numOfURLs)
			for _, url := range r.URL.Query()["u"] {
				go getNumbers(url, count, container)
			}

			select {
			case <-finished:
				// fmt.Println("finished")
			case <-timeout:
				// fmt.Println("time out", time.Since(start).Seconds())
			}
		} else {
			container = make(Container, 1)
			container[0].Numbers = make([]int, 0)
		}

		enc := json.NewEncoder(w)
		result := StateNums{
			Numbers: container[0].Numbers,
		}

		w.Header().Set("Content-Type", "application/json")
		// fmt.Println("before http write ", time.Since(start).Seconds())
		if err := enc.Encode(&result); err != nil {
			log.Println("Error while stream encoding", err)
		}
		// fmt.Println("after http write", time.Since(start).Seconds())
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// getNumbers collects numbers from a service.
// It sorts the received numbers initially and passes them to putNumbers.
// It uses the count channel if a ressource is not usable.
func getNumbers(url string, count chan bool, container Container) {
	// todo finished call to stop logging
	if !IsURL(url) {
		count <- true
		fmt.Println(url, " is not a valid url")
		return
	}

	// fmt.Println("Getting ", url)
	timeout := time.Duration(REQTIMEOUT * time.Millisecond)
	client := http.Client{
		Timeout: timeout,
	}
	resp, err := client.Get(url)
	if err != nil {
		count <- true
		fmt.Println("error connection: ", err)
		return
	}
	if resp.StatusCode != 200 {
		count <- true
		fmt.Println("server code: ", resp.StatusCode)
		return
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		count <- true
		fmt.Println("error reading response body: ", err)
		return
	}

	var result StateNums
	err = json.Unmarshal(body, &result)
	if err != nil {
		count <- true
		fmt.Println("error unmarshalling: ", err)
		return
	}

	sort.Ints(result.Numbers) // these days this seems to be as fast as implementing the sort.Interface
	// fmt.Println("Received from ", url)
	putNumbers(container, count, result.Numbers)
}

// putNumbers starts merging if it finds a partner for the numbers received from getNumbers.
// If if does not find a partner, the numbers will be placed in a space that is FREE
// and the place will be marked READY
func putNumbers(container Container, count chan bool, numbers []int) {
	// the goroutines are looking for two places to merge numbers from.
	// So this would be racy.
	mu.Lock()
	defer mu.Unlock()
	// The first slice of numbers to arrive have to send on the count channel.
	// Other sends on the count channel are triggered by successfull merges.
	if container[0].state == FREE {
		// in case it is the only bunch I have to dedoublicate the first
		// because other dedoubs happen at merging only
		numbersUnique := make([]int, len(numbers))
		numbersUnique[0] = numbers[0]
		j := 1
		for i := 1; i < len(numbers); i++ {
			if numbers[i-1] != numbers[i] {
				numbersUnique[j] = numbers[i]
				j++
			}
		}
		lengthCut := make([]int, j)
		copy(lengthCut, numbersUnique)
		container[0].Numbers = lengthCut
		container[0].state = READY
		count <- true
		return
	}

	foundpartner := false
	for i, statenums := range container {
		if statenums.state == READY {
			container[i].state = MERGING
			foundpartner = true
			// To go or not to go? I think it does not matter here.
			go mergeIndexAndNumbers(container, i, numbers, count)
			break
		}
	}
	if !foundpartner {
		for i, statenums := range container {
			if statenums.state == FREE {
				container[i].state = READY
				container[i].Numbers = numbers
				// fmt.Println("Have put numbers READY at: ", i)
				break
			}
		}
	}
}

// mergeIndexAndNumbers manages state and calls mergeUnique
func mergeIndexAndNumbers(container Container, indexA int, numsB []int, count chan bool) {
	numsA := container[indexA].Numbers
	container[indexA].Numbers = mergeUnique(numsA, numsB)
	container[indexA].state = READY // state was MERGING so no lock needed
	// fmt.Println("done merging ", indexA, " and nums")
	count <- true
	findPairs(container, count)
}

// mergeTwoIndexes manages state and calls mergeUnique
func mergeTwoIndexes(container Container, indexA int, indexB int, count chan bool) {
	numsA := container[indexA].Numbers
	numsB := container[indexB].Numbers
	container[indexA].Numbers = mergeUnique(numsA, numsB)
	container[indexA].state = READY // state was MERGING so no lock needed
	container[indexB].state = FREE  // state was MERGING so no lock needed
	// fmt.Println("done merging ", indexA, " and ", indexB)
	count <- true
	findPairs(container, count)
}

// findPairs looks for two places that have state READY
func findPairs(container Container, count chan bool) {
	// fmt.Println("trying to find two waiting")
	indexASet := false
	var indexA int
	// The goroutines are looking for two places to merge numbers from.
	// So this would be racy.
	mu.Lock()
	defer mu.Unlock()
	for i, statenums := range container {
		if statenums.state == READY && !indexASet {
			indexA = i
			indexASet = true
		} else if statenums.state == READY && indexASet {
			// fmt.Println("Found two READY ", indexA, " ", i)
			container[indexA].state = MERGING
			container[i].state = MERGING
			go mergeTwoIndexes(container, indexA, i, count) // merge two indexes
			break
		}
	}
}

// mergeUnique is the usual merge two sorted arrays algorithm. But with a twist:
// If the predecessor is equal it will not increment the destination index.
func mergeUnique(a []int, b []int) []int {
	c := make([]int, len(a)+len(b))
	i, j, k := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] < b[j] {
			c[k] = a[i]
			i++
		} else {
			c[k] = b[j]
			j++
		}
		if !(k > 0 && c[k-1] == c[k]) {
			k++
		}
	}

	for i < len(a) {
		c[k] = a[i]
		i++
		if !(k > 0 && c[k-1] == c[k]) {
			k++
		}
	}

	for j < len(b) {
		c[k] = b[j]
		j++
		if !(k > 0 && c[k-1] == c[k]) {
			k++
		}
	}
	d := make([]int, k)
	copy(d, c)
	return d
}

// counter counts up to the urls provided as arguments in the request.
// It counts malformed URLs, request- and JSON-errors and successfull merges.
// It sends on the finished channel if all is done.
func counter(count chan bool, finished chan bool, numOfURLs int) {
	n := 0
	for {
		<-count
		n++
		if n == numOfURLs {
			finished <- true
			return
		}
	}
}

// IsURL is taken from https://github.com/asaskevich/govalidator/blob/master/validator.go
// I can not come up with an own function. You probably already have a good one.
// I also would suggest to log urls that fail validation and then see what we can improve.
func IsURL(str string) bool {
	if str == "" || len(str) >= 2083 || len(str) <= 3 || strings.HasPrefix(str, ".") {
		return false
	}
	u, err := url.Parse(str)
	if err != nil {
		return false
	}
	if strings.HasPrefix(u.Host, ".") {
		return false
	}
	if u.Host == "" && (u.Path != "" && !strings.Contains(u.Path, ".")) {
		return false
	}
	return rxURL.MatchString(str)
}
