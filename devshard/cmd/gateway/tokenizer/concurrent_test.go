package tokenizer

import (
	"fmt"
	"sync"
	"testing"
)

const (
	concurrentWorkers = 8
	concurrentRounds  = 3
)

var concurrentSizes = []int{200, 4 << 10, 64 << 10}

// Test flow:
//  1. Load every testdata tokenizer and build texts of every bench kind and size, the golden texts and the added-token texts.
//  2. Count every tokenizer and text on one goroutine for the expected counts.
//  3. Count them all again from concurrentWorkers goroutines at once, each starting at its own offset, so the pooled memo tables pass between goroutines, tokenizers and sizes.
//  4. Assert every concurrent count equals the single-threaded one.
func TestConcurrentCountsMatchSequentialOnes(t *testing.T) {
	var texts []string
	for _, kind := range benchKinds {
		for _, size := range concurrentSizes {
			texts = append(texts, benchText(kind, size))
		}
	}
	texts = append(texts, goldenTexts...)
	for _, cases := range addedTokenCounts {
		for _, test := range cases {
			texts = append(texts, test.text)
		}
	}
	type job struct {
		name    string
		counter *Counter
		text    string
		want    int
	}
	var jobs []job
	for _, name := range differentialTestdataNames {
		counter := loadTestdata(t, name)
		for _, text := range texts {
			want, err := counter.Count(text)
			if err != nil {
				t.Fatal(err)
			}
			jobs = append(jobs, job{name, counter, text, want})
		}
	}

	failures := make(chan string, len(jobs)*concurrentWorkers*concurrentRounds)
	var workers sync.WaitGroup
	for worker := range concurrentWorkers {
		workers.Go(func() {
			for round := range concurrentRounds {
				for index := range jobs {
					current := jobs[(index+worker*len(jobs)/concurrentWorkers+round)%len(jobs)]
					got, err := current.counter.Count(current.text)
					if err != nil || got != current.want {
						failures <- fmt.Sprintf("%s, %d-byte text: Count = %d, %v, want %d", current.name, len(current.text), got, err, current.want)
					}
				}
			}
		})
	}
	workers.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
}
