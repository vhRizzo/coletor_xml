package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Debug      bool
	Simulation bool
	DBHost     string
	DBPort     int
	DBService  string
	DBUser     string
	DBPassword string
}

// Utilizar essa função caso o docker compose seja finalizado
// func readSecret(name string) string {
// 	// O Docker monta os segredos em /run/secrets/
// 	content, err := os.ReadFile("/run/secrets/" + name)
// 	if err != nil {
// 		// Em ambiente de desenvolvimento, onde o arquivo não existe, lê de uma variável de ambiente como fallback para desenvolvimento local.
// 		log.Printf("Aviso: não foi possível ler o segredo '%s'. Tentando como variável de ambiente.", name)
// 		return os.Getenv(name)
// 	}
// 	// Remove espaços em branco e quebras de linha que possam existir no arquivo.
// 	return strings.TrimSpace(string(content))
// }

func LoadEnv() Config {
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("erro crítico: não foi possível encontrar o caminho do executável: %v", err)
	}

	rootDir := filepath.Dir(exePath)
	envPath := filepath.Join(rootDir, ".env")

	err = godotenv.Load(envPath)
	if err != nil {
		log.Printf("não foi possível carregar o arquivo .env de '%s'. Usando variáveis de ambiente do sistema, se existirem. Erro: %v", envPath, err)
	}

	debug, _ := strconv.ParseBool(os.Getenv("DEBUG"))
	simulation, _ := strconv.ParseBool(os.Getenv("SIMULATION"))
	port, _ := strconv.Atoi(os.Getenv("DB_PORT"))
	return Config{
		Debug:      debug,
		Simulation: simulation,
		DBHost:     os.Getenv("DB_HOST"),
		DBPort:     port,
		DBService:  os.Getenv("DB_SERVICE"),
		DBUser:     os.Getenv("DB_USER"),
		DBPassword: os.Getenv("DB_PASSWORD"),
	}
}
