package telegrambot


import (
  "encoding/json"
  "fmt"
  "html"
  "net/http"

  "github.com/GoogleCloudPlatform/functions-framework-go/functions"
)

const telegram_bot_token = "8262388367:AAGftOurwzHDflBtNH6pRUH6CL-tg8Tongg"

func init() {
	functions.HTTP("TelegramHook", telegram_hook)
   // functions.HTTP("HelloHTTP", helloHTTP)
}


func telegram_hook(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
