package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var paymentChannel chan []byte = make(chan []byte, 100000)
var errChannel chan *Payment = make(chan *Payment, 100000)

var mu sync.Mutex = sync.Mutex{}
var data map[time.Time][]Payment = make(map[time.Time][]Payment)
var workerMutex *sync.Mutex = &sync.Mutex{}
var errWorkerMutex *sync.Mutex = &sync.Mutex{}

func mutualLock() {
	workerMutex.Lock()
	errWorkerMutex.Lock()
}

func mutualUnlock() {
	errWorkerMutex.Unlock()
	workerMutex.Unlock()
}

var instances []string = []string{}

var principal string = ""
var fallback string = ""

var client *http.Client = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DisableKeepAlives:   false,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	},
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // cuidado: permite conexões de qualquer origem
	},
}

type Payment struct {
	ID          string  `json:"correlationId"`
	Amount      float64 `json:"amount"`
	RequestedAt string  `json:"requestedAt"` //2025-07-15T12:34:56.000Z
	IsDefault   bool    `json:"-"`
	Err         string  `json:"-"`
	Attempts    int     `json:"-"`
}

type PaymentSummary struct {
	Default  Summary `json:"default"`
	Fallback Summary `json:"fallback"`
}

type Summary struct {
	TotalRequests int     `json:"totalRequests"`
	TotalAmount   float64 `json:"totalAmount"`
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("Read error:", err)
			continue
		}
		paymentChannel <- msg
	}
}

func Save(payment *Payment) error {
	mu.Lock()
	defer mu.Unlock()

	t, err := time.Parse(time.RFC3339, payment.RequestedAt)
	if err != nil {
		return err
	}

	if _, exists := data[t]; !exists {
		data[t] = []Payment{
			*payment,
		}
		return nil
	}

	data[t] = append(data[t], *payment)
	return nil
}

func GetSummary(from, to *time.Time) PaymentSummary {
	mu.Lock()
	defer mu.Unlock()

	if from == nil && to == nil {
		return GetAllPaymentsSummary()
	}
	if from != nil && to != nil {
		return GetFromToPaymentsSummary(from, to)
	}
	if from != nil {
		return GetFromPaymentsSummary(from)
	}
	return GetToPaymentsSummary(to)
}

func GetFromPaymentsSummary(from *time.Time) PaymentSummary {
	defaultSummary := Summary{}
	fallbackSummary := Summary{}

	for t, payments := range data {
		if t.After(*from) {
			for _, payment := range payments {
				if payment.IsDefault {
					defaultSummary.TotalRequests++
					defaultSummary.TotalAmount += payment.Amount
				} else {
					fallbackSummary.TotalRequests++
					fallbackSummary.TotalAmount += payment.Amount
				}
			}
		}
	}

	return PaymentSummary{
		Default:  defaultSummary,
		Fallback: fallbackSummary,
	}
}

func GetToPaymentsSummary(to *time.Time) PaymentSummary {
	defaultSummary := Summary{}
	fallbackSummary := Summary{}

	for t, payments := range data {
		if t.Before(*to) {
			for _, payment := range payments {
				if payment.IsDefault {
					defaultSummary.TotalRequests++
					defaultSummary.TotalAmount += payment.Amount
				} else {
					fallbackSummary.TotalRequests++
					fallbackSummary.TotalAmount += payment.Amount
				}
			}
		}
	}
	return PaymentSummary{
		Default:  defaultSummary,
		Fallback: fallbackSummary,
	}
}

func GetFromToPaymentsSummary(from, to *time.Time) PaymentSummary {
	defaultSummary := Summary{}
	fallbackSummary := Summary{}

	for t, payments := range data {
		if t.After(*from) && t.Before(*to) {
			for _, payment := range payments {
				if payment.IsDefault {
					defaultSummary.TotalRequests++
					defaultSummary.TotalAmount += payment.Amount
				} else {
					fallbackSummary.TotalRequests++
					fallbackSummary.TotalAmount += payment.Amount
				}
			}
		}
	}

	return PaymentSummary{
		Default:  defaultSummary,
		Fallback: fallbackSummary,
	}
}

func GetAllPaymentsSummary() PaymentSummary {
	defaultSummary := Summary{}
	fallbackSummary := Summary{}

	for _, payments := range data {
		for _, payment := range payments {
			if payment.IsDefault {
				defaultSummary.TotalRequests++
				defaultSummary.TotalAmount += payment.Amount
			} else {
				fallbackSummary.TotalRequests++
				fallbackSummary.TotalAmount += payment.Amount
			}
		}
	}

	return PaymentSummary{
		Default:  defaultSummary,
		Fallback: fallbackSummary,
	}
}

func GetInstanceSummary(instance string, from, to string) (PaymentSummary, error) {

	if instance == "" {
		return PaymentSummary{}, fmt.Errorf("instance cannot be empty")
	}

	url := fmt.Sprintf("%s/payments-summary?internal=true", instance)
	if from != "" {
		url += fmt.Sprintf("&from=%s", from)
	}

	if to != "" {
		url += fmt.Sprintf("&to=%s", to)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return PaymentSummary{}, fmt.Errorf("error creating request: %v", err)
	}

	fmt.Println("Requesting summary from:", url)

	res, err := client.Do(req)
	if err != nil {
		return PaymentSummary{}, fmt.Errorf("error sending request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return PaymentSummary{}, fmt.Errorf("failed to get summary from %s, status code: %d", instance, res.StatusCode)
	}

	var summary PaymentSummary
	if err := json.NewDecoder(res.Body).Decode(&summary); err != nil {
		return PaymentSummary{}, fmt.Errorf("error decoding response: %v", err)
	}

	return summary, nil
}

func handleGetSummary(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	internal := r.URL.Query().Get("internal")

	fmt.Println("Received summary request - from:", from, "to:", to, "internal:", internal)

	dateFormat := "2006-01-02T15:04:05.000Z"

	var fromTime, toTime *time.Time = nil, nil

	if from != "" {
		parsedFrom, err := time.Parse(dateFormat, from)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fromTime = &parsedFrom
	}

	if to != "" {
		parsedTo, err := time.Parse(dateFormat, to)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		toTime = &parsedTo
	}

	mutualLock()
	defer mutualUnlock()

	summary := GetSummary(fromTime, toTime)

	if internal == "" {
		for _, instance := range instances {

			if instance == "" {
				continue
			}

			instanceSummary, err := GetInstanceSummary(instance, from, to)
			if err != nil {
				continue
			}

			summary.Default.TotalRequests += instanceSummary.Default.TotalRequests
			summary.Default.TotalAmount += instanceSummary.Default.TotalAmount
			summary.Fallback.TotalRequests += instanceSummary.Fallback.TotalRequests
			summary.Fallback.TotalAmount += instanceSummary.Fallback.TotalAmount
		}
	}

	summary.Default.TotalAmount = roundFloat64(summary.Default.TotalAmount, 2)
	summary.Fallback.TotalAmount = roundFloat64(summary.Fallback.TotalAmount, 2)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(summary); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func roundFloat64(x float64, decimals int) float64 {
	factor := math.Pow(10, float64(decimals))
	return math.Round(x*factor) / factor
}

func SendPayment(payment *Payment) (error, bool) {

	if payment == nil {
		return fmt.Errorf("payment cannot be nil"), false
	}

	var err error

	payment.Attempts++

	if err = send(principal, payment); err != nil {
		if payment.Attempts > 3 {
			if err = send(fallback, payment); err != nil {
				return fmt.Errorf("failed to send payment to both services: %v", err), false
			}
			return nil, false
		} else {
			return err, false
		}
	}

	return nil, true

}

func send(url string, payment *Payment) error {

	body, err := json.Marshal(payment)
	if err != nil {
		return fmt.Errorf("error marshalling payment: %v", err)
	}

	if url == "" {
		return fmt.Errorf("URL cannot be empty")
	}

	url = fmt.Sprintf("%s/payments", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending payment to principal service: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send payment to principal service, status code: %d", res.StatusCode)
	}

	return nil
}

func worker() {
	for b := range paymentChannel {
		func() {

			workerMutex.Lock()
			workerMutex.Unlock()

			var err error

			var p *Payment = &Payment{}
			if err = json.Unmarshal(b, p); err != nil {
				fmt.Println("Error unmarshalling payment:", err)
				return
			}

			p.RequestedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

			if p.Err != "SAVE" {
				err, p.IsDefault = SendPayment(p)
				if err != nil {
					errChannel <- p
					return
				}
			}

			workerMutex.Lock()
			workerMutex.Unlock()

			err = Save(p)
			if err != nil {
				fmt.Println("Error saving payment:", err)
				p.Err = "SAVE"
				errChannel <- p
			}
		}()
	}
}

func errWorker() {
	for p := range errChannel {
		func() {

			errWorkerMutex.Lock()
			errWorkerMutex.Unlock()

			var err error
			p.RequestedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

			if p.Err != "SAVE" {
				err, p.IsDefault = SendPayment(p)
				if err != nil {
					errChannel <- p
					return
				}
			}

			errWorkerMutex.Lock()
			errWorkerMutex.Unlock()

			err = Save(p)
			if err != nil {
				fmt.Println("Error saving payment:", err)
				p.Err = "SAVE"
				errChannel <- p
			}
		}()
	}
}

func main() {

	instancesStr := os.Getenv("API_INSTANCES")
	if instancesStr == "" {
		fmt.Println("API_INSTANCES environment variable must be set")
		return
	}

	instances = strings.Split(instancesStr, ",")

	principal = os.Getenv("PRINCIPAL_SERVICE")
	fallback = os.Getenv("FALLBACK_SERVICE")
	if principal == "" || fallback == "" {
		fmt.Println("PRINCIPAL_SERVICE and FALLBACK_SERVICE environment variables must be set")
		return
	}

	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/payments-summary", handleGetSummary)

	go worker()
	go errWorker()

	fmt.Println("Listening on port 8080...")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Server error:", err)
	}
}
