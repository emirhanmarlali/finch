package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"

	"github.com/emreler/finch/auth"
	"github.com/emreler/finch/config"
	"github.com/emreler/finch/counter"
	"github.com/emreler/finch/handlers"
	"github.com/emreler/finch/logger"
	"github.com/emreler/finch/storage"
	"github.com/gorilla/websocket"
)

const prefix = "/v1"

func main() {
	configPath := flag.String("config", "", "Path of config.json file")
	flag.Parse()

	config := config.NewConfig(*configPath)

	alertChannel := make(chan string)
	counterChannel := make(chan bool)

	auth := auth.NewAuth(config.Secret)
	stg := storage.NewStorage(config.Mongo)
	alerter := storage.NewAlerter(config.Redis, &alertChannel)
	logger := logger.NewLogger(config.Logentries)

	hnd := handlers.NewHandlers(stg, alerter, logger, auth, counterChannel)

	processedAlertCount, err := stg.CountProcessAlertLogs()
	if err != nil {
		panic(err)
	}

	alerter.StartListening()

	mux := http.NewServeMux()

	// serving homepage
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() == "/" {
			// request for index.html, parse template with counter value
			t := template.New("index.html")
			t, err := t.ParseFiles("web/index.html")

			if err != nil {
				logger.Error(err)
			}

			vars := struct {
				Counter int
			}{
				processedAlertCount,
			}

			t.Execute(w, vars)
			return
		}

		http.FileServer(http.Dir("web")).ServeHTTP(w, r)
	})

	// serving api
	mux.Handle(prefix+"/alerts/", handlers.FinchHandler(hnd.AlertDetail))
	mux.Handle(prefix+"/alerts", handlers.FinchHandler(hnd.Alerts))
	mux.Handle(prefix+"/users", handlers.FinchHandler(hnd.CreateUser))

	hub := counter.NewHub()
	go hub.Run()

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// new incoming ws connction
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true }, // allow connections from all origins
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error(err)
			return
		}

		client := &counter.Client{Conn: conn, Send: make(chan []byte)}
		hub.Register <- client

		client.WaitMessages()
	})

	go func() {
		for {
			<-counterChannel
			processedAlertCount++
			// increment the counter on clients
			hub.Broadcast <- []byte(strconv.Itoa(processedAlertCount))
		}
	}()

	go func() {
		for {
			alertID := <-alertChannel
			go func() {
				hnd.ProcessAlert(alertID)
			}()
		}
	}()

	log.Println("Starting server")
	fmt.Println(http.ListenAndServe(":8081", mux))

}
