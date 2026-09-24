package main

import (
	"context"
	"log"
	"time"

	telegrambot "github.com/untimated/telegram-bot"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := telegrambot.RunDailyWeatherDigest(ctx); err != nil {
		log.Fatal(err)
	}
}
