package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-redis/redis"
)

func main() {
	redisdb := redis.NewClient(&redis.Options{
		Addr: os.Getenv("REDIS_ADDR"),
	})
	topic := os.Getenv("REDIS_TOPIC")

	_, err := redisdb.Ping().Result()
	if err != nil {
		log.Fatal(err)
	}

	count := 0
	for {
		if count == 2 {
			break
		}

		res, err := redisdb.BLPop(15*time.Second, topic).Result()
		if err != nil {
			if err != redis.Nil {
				log.Fatal(err)
			} else {
				count++
				continue
			}
		}
		fmt.Printf("Processed topic %s: %s\n", topic, res[1])
	}
	fmt.Println("going to shutdown as no work needs to be done anymore")
}
