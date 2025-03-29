package utils

import "github.com/google/uuid"

func GetUuid() string {
	return uuid.New().String() // 生成唯一的 UUID
}
