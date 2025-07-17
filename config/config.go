package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Simulation bool
	DBHost     string
	DBPort     string
	DBService  string
	DBUser     string
	DBPassword string
}

func readSecret(name string) string {
	// O Docker monta os segredos em /run/secrets/
	content, err := os.ReadFile("/run/secrets/" + name)
	if err != nil {
		// Em ambiente de desenvolvimento, onde o arquivo não existe, lê de uma variável de ambiente como fallback para desenvolvimento local.
		log.Printf("Aviso: não foi possível ler o segredo '%s'. Tentando como variável de ambiente.", name)
		return os.Getenv(strings.ToUpper(name))
	}
	// Remove espaços em branco e quebras de linha que possam existir no arquivo.
	return strings.TrimSpace(string(content))
}

func LoadEnv() Config {
	godotenv.Load()

	simulation, _ := strconv.ParseBool(readSecret("simulation"))
	return Config{
		Simulation: simulation,
		DBHost:     readSecret("db_host"),
		DBPort:     readSecret("db_port"),
		DBService:  readSecret("db_service"),
		DBUser:     readSecret("db_user"),
		DBPassword: readSecret("db_password"),
	}
}
