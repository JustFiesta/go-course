package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type ApiResponse struct {
	Status       string             `json:"status"`
	Page         string             `json:"page"`
	Words        []string           `json:"words"`
	Percentages  map[string]float64 `json:"percentages"`
	Special      []any              `json:"special"`
	ExtraSpecial []any              `json:"extraSpecial"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <API_URL>")
		os.Exit(1)
	}
	url := os.Args[1]

	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Error fetching URL:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Non-OK HTTP status: %s\n", resp.Status)
		os.Exit(1)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		os.Exit(1)
	}

	var response ApiResponse
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Println("Error parsing JSON:", err)
		os.Exit(1)
	}

	// Dodaj status na podstawie kodu HTTP
	response.Status = resp.Status

	timestamp := resp.Header.Get("Date")
	if timestamp == "" {
		timestamp = fmt.Sprintf("%v", resp.Header)
	}

	fmt.Printf("Parsed response:\nStatus: %s\nTimestamp: %s\nPage: %s\nWords: %v\nPercentages: %v\nSpecial: %v\nExtraSpecial: %v\n",
		response.Status, timestamp, response.Page, response.Words, response.Percentages, response.Special, response.ExtraSpecial)
}
