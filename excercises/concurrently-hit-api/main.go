package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

func makeRequest(url string, resultCh chan<- string) {
	resp, err := http.Get(url)
	if err != nil {
		resultCh <- fmt.Sprintf("Error making request: %v", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resultCh <- fmt.Sprintf("Error reading body: %v", err)
		return
	}
	resultCh <- string(body)
}

func fire5ConcurrentRequests(url string) (responses []string) {
	var syncGroup sync.WaitGroup
	start := make(chan bool)
	resultCh := make(chan string, 5)
	for i := 0; i < 5; i++ {
		syncGroup.Add(1)
		go func(id int) {
			defer syncGroup.Done()
			<-start
			makeRequest(url, resultCh)
		}(i + 1)
	}
	close(start)
	syncGroup.Wait()
	close(resultCh)
	for res := range resultCh {
		responses = append(responses, res)
	}
	return
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <API_URL>")
		os.Exit(1)
	}
	var url string = os.Args[1]

	fmt.Println("Hitting API on URL:", url)

	for {
		responses := fire5ConcurrentRequests(url)
		for _, res := range responses {
			fmt.Println(res)
			if len(res) > 0 && (res[:4] == "DONE" || res[:5] == "DONE!") {
				fmt.Println("Success!")
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
}
