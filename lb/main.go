package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/gorilla/websocket"
)

var post bool = false
var muPost sync.Mutex = sync.Mutex{}
var get bool = false
var muGet sync.Mutex = sync.Mutex{}

var muWs1 sync.Mutex = sync.Mutex{}
var muWs2 sync.Mutex = sync.Mutex{}

var wsApi1, wsApi2 *websocket.Conn
var httpApi1, httpApi2 string

func handlePost(c *fiber.Ctx) error {

	body := c.Body()

	sendToApi1 := false
	muPost.Lock()
	sendToApi1 = post
	post = !post
	muPost.Unlock()

	if sendToApi1 {
		muWs1.Lock()
		if err := wsApi1.WriteMessage(websocket.TextMessage, body); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to send message to API1: " + err.Error())
		}
		muWs1.Unlock()
	} else {
		muWs2.Lock()
		if err := wsApi2.WriteMessage(websocket.TextMessage, body); err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to send message to API2: " + err.Error())
		}
		muWs2.Unlock()
	}

	return c.SendStatus(fiber.StatusOK)
}

func handleGet(c *fiber.Ctx) error {
	muGet.Lock()
	useAPI1 := get
	get = !get
	muGet.Unlock()

	var targetURL string
	if useAPI1 {
		targetURL = httpApi1
	} else {
		targetURL = httpApi2
	}

	targetURL += "/payments-summary"

	query := c.Request().URI().QueryString()
	if len(query) > 0 {
		targetURL += "?" + string(query)
	}

	fmt.Println("Fetching from:", targetURL)

	resp, err := http.Get(targetURL)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).SendString("Erro ao requisitar API: " + err.Error())
	}
	defer resp.Body.Close()

	// Lê corpo da resposta
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Erro ao ler resposta da API: " + err.Error())
	}

	// Retorna conteúdo como resposta do seu proxy
	return c.Status(resp.StatusCode).Send(body)
}

func getWsUrl(host string) string {
	return "ws://" + host + "/ws"
}

func getHttpUrl(host string) string {
	return "http://" + host
}

func main() {
	app := fiber.New()

	api1 := os.Getenv("API1")
	api2 := os.Getenv("API2")
	if api1 == "" || api2 == "" {
		panic("API1 and API2 environment variables must be set")
	}

	apiWebSocket1Url := getWsUrl(api1)
	apiWebSocket2Url := getWsUrl(api2)
	httpApi1 = getHttpUrl(api1)
	httpApi2 = getHttpUrl(api2)

	var err error
	wsApi1, _, err = websocket.DefaultDialer.Dial(apiWebSocket1Url, nil)
	if err != nil {
		panic("Failed to connect to API1 WebSocket: " + err.Error())
	}
	wsApi2, _, err = websocket.DefaultDialer.Dial(apiWebSocket2Url, nil)
	if err != nil {
		panic("Failed to connect to API2 WebSocket: " + err.Error())
	}

	app.Post("/payments", handlePost)
	app.Get("/payments-summary", handleGet)

	app.Listen(":9999")
}
