package syncx

import "sync"

// Fan runs the work over every item at once and answers in the order the items were
// given, so a slow item costs one wait for all of them rather than one each
func Fan[In, Out any](items []In, work func(In) Out) []Out {
	answers := make([]Out, len(items))

	var running sync.WaitGroup
	for index, item := range items {
		running.Add(1)
		go func() {
			defer running.Done()
			answers[index] = work(item)
		}()
	}
	running.Wait()
	return answers
}
