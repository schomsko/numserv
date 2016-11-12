# numserv

The service receives numbers from different servers at different points in time. So it can start merging numbers as soon as there are at least two bunches of them. 
So the service uses an array as container where each place can hold a struct that contains a state and the numbers that have to be merged. The state shows if the place is free (FREE) or if it contains either numbers that are waiting (READY) to be merged or that are currently beeing merged (MERGING). 

So let's take a walk through the process. 
When a new bunch of numbers arrive, the service takes a look, if there is already a place with the state READY - because then there are some numbers waiting to be merged. 
-> If there are some, then the service merges the two bunches. But before the merge It sets the state from READY to MERGING and after the merging it sets the state back to READY again.
-> If there are no other numbers READY, the service finds the next free space from the left and saves the newly arrived bunch of numbers there.
If a merging was done, the service takes a look, if there is another bunch of numbers waiting with the state READY. Because they might have arrived while the merging was going on. 

So the system has two events that trigger the look for merging candidates 
-> new arrivals of number bunches 
-> some merging finished 

The looking-for-candidates-loop start from the left and the leftmost place is always the first to try to merge with others. So it likely will be the most merged numbers. That left place is also the place where the merged result can be found if all the fetching and merging is finished before the timeout. But sometimes it will be that some services answered quite late and there are some interim merges waiting on not so left places when the timeout hits. So they will be left behind. 

The service answers as soon as possible. But depending on the input, it can happen, that the service responds to late. 

