package config

import (
	"errors"
	"time"

	"docmind/internal/llm"
)

func Chat(getenv func(string) string) (llm.ChatProvider, error) {
	provider := getenv("CHAT_PROVIDER")
	if provider == "" || provider == "fake" {
		return llm.FakeChat{}, nil
	}
	if provider != "yandex" {
		return nil, errors.New("CHAT_PROVIDER must be fake or yandex")
	}
	timeout := 10 * time.Second
	if value := getenv("CHAT_HTTP_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > time.Minute {
			return nil, errors.New("CHAT_HTTP_TIMEOUT must be a positive duration up to 1m")
		}
	}
	return llm.NewYandexChat(getenv("YANDEX_API_KEY"), getenv("YANDEX_FOLDER_ID"), timeout)
}

func QuestionTimeout(getenv func(string) string, writeTimeout time.Duration) (time.Duration, error) {
	timeout := 20 * time.Second
	if value := getenv("QUESTION_TIMEOUT"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return 0, errors.New("QUESTION_TIMEOUT must be a positive duration")
		}
	}
	if timeout >= writeTimeout {
		return 0, errors.New("QUESTION_TIMEOUT must be less than HTTP_WRITE_TIMEOUT")
	}
	return timeout, nil
}
