package store

import (
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisClientConfig struct {
	Addr     string
	Password string
	DB       int
}

func NewRedisClient(cfg RedisClientConfig) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
}

