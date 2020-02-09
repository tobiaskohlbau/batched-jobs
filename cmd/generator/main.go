package main

import (
	"log"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/go-redis/redis"
)

func main() {
	redisdb := redis.NewClient(&redis.Options{
		Addr: os.Getenv("REDIS_ADDR"),
	})
	_, err := redisdb.Ping().Result()
	if err != nil {
		log.Fatal(err)
	}

	topics := []string{"topic1", "topic2", "topic3"}

	for {
		topicSelector := rand.Intn(3)
		work := rand.Intn(100)
		if err := redisdb.RPush(topics[topicSelector], strconv.Itoa(work)).Err(); err != nil {
			log.Fatal(err)
		}
		if err := redisdb.Publish("informer", topics[topicSelector]).Err(); err != nil {
			log.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
