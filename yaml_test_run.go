package main

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func main() {
	yamlData := `
port: 8317
postgres:
  dsn: "postgres://cliproxy@127.0.0.1:5432/cliproxy?sslmode=disable"
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Postgres DSN: %v\n", cfg.Postgres.DSN)
}
