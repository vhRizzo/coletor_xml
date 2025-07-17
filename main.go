package main

import (
	"coletor_xml/config"
	"coletor_xml/db"
	"coletor_xml/email"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	// 1. Contexto para orquestrar o desligamento gracioso.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Garante que cancel() seja chamado no fim, por segurança.

	cfg := config.LoadEnv()
	dbConn, err := db.ConnectOracle(cfg)
	if err != nil {
		log.Fatalf("Erro fatal ao conectar no banco: %v", err)
	}
	defer dbConn.Close()

	// 2. Canal para o runMonitoring nos avisar que terminou o desligamento.
	monitoringDone := make(chan struct{})

	// 3. Inicia o monitoramento em uma goroutine separada.
	go func() {
		defer close(monitoringDone) // Avisa que terminou ao sair.
		if err := runMonitoring(ctx, cfg, dbConn); err != nil {
			log.Printf("erro crítico ao iniciar o monitoramento: %v", err)
		}
	}()

	// 4. Aguarda por um sinal de interrupção do sistema operacional.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("aplicação iniciada. Pressione Ctrl+C para encerrar.")

	select {
	case sig := <-sigChan:
		log.Printf("sinal recebido: %v. Iniciando desligamento gracioso...", sig)
		cancel() // Avisa todas as goroutines para pararem.

	case <-monitoringDone:
		// Isso aconteceria se o runMonitoring falhasse no setup e encerrasse prematuramente.
		log.Println("monitoramento encerrado inesperadamente.")
	}

	// 5. Dá um tempo para o desligamento gracioso acontecer, ou força o encerramento.
	select {
	case <-monitoringDone:
		log.Println("desligamento gracioso concluído com sucesso.")
	case <-time.After(10 * time.Second):
		log.Println("timeout no desligamento gracioso. Forçando encerramento.")
	}

	log.Println("aplicação encerrada.")
}

func runMonitoring(ctx context.Context, cfg config.Config, dbConn *sql.DB) error {
	usuarios, err := db.ListarUsuarios(dbConn)
	if err != nil {
		return fmt.Errorf("erro ao buscar usuários: %w", err)
	}

	var wg sync.WaitGroup
	for _, user := range usuarios {
		if ctx.Err() != nil {
			log.Println("contexto cancelado antes de iniciar todos os workers.")
			break
		}
		wg.Add(1)
		go func(u db.UsuarioEmail) {
			defer wg.Done()
			monitorUser(ctx, u, cfg, dbConn)
		}(user)
	}

	log.Println("aguardando todas as rotinas de monitoramento encerrarem...")
	wg.Wait()
	log.Println("todas as rotinas de monitoramento foram encerradas.")
	return nil
}

func monitorUser(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) {
	log.Printf("iniciando worker para o usuário: %s", u.Usuario)

	switch u.Protocolo {
	case "I":
		email.MonitorarIMAP(ctx, u, cfg, conn)
	case "P":
		email.MonitorarPOP3(ctx, u, cfg, conn)
	default:
		log.Printf("protocolo não reconhecido para %s. Encerrando worker.", u.Usuario)
	}

	log.Printf("Worker para o usuário %s foi encerrado graciosamente.", u.Usuario)
}
