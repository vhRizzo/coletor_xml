package main

import (
	"bytes"
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
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceName = "ColetorXML"

// coletorService implementa a interface svc.Handler para o nosso serviço.
type coletorService struct{}

// eventLogWriter é um adaptador que faz o *eventlog.Log se comportar como um io.Writer.
type eventLogWriter struct {
	elog *eventlog.Log
}

func main() {
	// Verifica se o programa está sendo executado como um serviço ou interativamente.
	isService, err := svc.IsWindowsService()
	if err != nil {
		log.Fatalf("Falha ao determinar se a sessão é interativa: %v", err)
	}

	if isService {
		// Inicializa o logger para o Visualizador de Eventos do Windows.
		elog, err := eventlog.Open(serviceName)
		if err != nil {
			// Se não conseguirmos abrir o log de eventos, não há muito o que fazer.
			// Normalmente, isso só falharia por problemas de permissão.
			return
		}
		defer elog.Close()

		writerAdaptador := &eventLogWriter{elog: elog}
		log.SetOutput(writerAdaptador)

		// Caminho de execução como SERVIÇO do Windows
		log.Println("iniciando em modo de serviço...")
		if err := svc.Run(serviceName, &coletorService{}); err != nil {
			log.Fatalf("falha ao rodar o serviço '%s': %v", serviceName, err)
		}
		return
	}

	// Caminho de execução INTERATIVO (no terminal)
	log.Println("rodando em modo interativo (terminal)...")
	ctx, cancel := context.WithCancel(context.Background())

	// Lógica de captura de sinal (Ctrl+C) para o modo interativo
	sigChan := make(chan os.Signal, 1)
	// No Windows, os.Interrupt (Ctrl+C) é o sinal mais relevante para o terminal.
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		<-sigChan
		log.Println("sinal de interrupção recebido, encerrando...")
		cancel()
	}()

	runApplication(ctx)
}

// Execute é o ponto de entrada principal quando o programa é iniciado como um serviço.
func (s *coletorService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	// Informa ao SCM que aceitamos os comandos de parada e desligamento.
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Inicia a lógica principal da aplicação em uma goroutine separada.
	go runApplication(ctx)

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	log.Printf("serviço '%s' iniciado com sucesso.", serviceName)

	// Loop para aguardar comandos do SCM do Windows
	for {
		// Aguarda o próximo comando de controle
		c := <-r
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			// O Windows pediu para o serviço parar.
			log.Printf("comando de parada recebido do SCM...")
			changes <- svc.Status{State: svc.StopPending}
			cancel() // Sinaliza para as goroutines pararem
			// Espera um pouco para o desligamento gracioso acontecer
			time.Sleep(5 * time.Second)
			log.Println("serviço parado.")
			return
		default:
			log.Printf("comando inesperado do SCM: %d", c)
		}
	}
}

func (w *eventLogWriter) Write(b []byte) (int, error) {
	// O log.Println adiciona uma quebra de linha no final, vamos removê-la.
	msg := string(bytes.TrimSpace(b))

	// Escreve a mensagem como um evento de "Informação" com um ID genérico (1).
	return len(b), w.elog.Info(1, msg)
}

func runApplication(ctx context.Context) {
	log.Println("carregando configuração e conectando ao banco...")
	cfg := config.LoadEnv()
	dbConn, err := db.ConnectOracle(cfg)
	if err != nil {
		if cfg.Debug {
			log.Printf("erro fatal ao conectar no banco: %v", err)
		}
		return
	}
	defer dbConn.Close()

	if err := runMonitoring(ctx, cfg, dbConn); err != nil {
		if cfg.Debug {
			log.Printf("monitoramento falhou: %v", err)
		}
	}

	<-ctx.Done()
	if cfg.Debug {
		log.Println("aplicação finalizando o desligamento.")
	}
}

func runMonitoring(ctx context.Context, cfg config.Config, dbConn *sql.DB) error {
	usuarios, err := db.ListarUsuarios(dbConn)
	if err != nil {
		return fmt.Errorf("erro ao buscar usuários: %w", err)
	}

	var wg sync.WaitGroup
	for _, user := range usuarios {
		if ctx.Err() != nil {
			if cfg.Debug {
				log.Println("contexto cancelado antes de iniciar todos os workers.")
			}
			break
		}
		wg.Add(1)
		go func(u db.UsuarioEmail) {
			defer wg.Done()
			monitorUser(ctx, u, cfg, dbConn)
		}(user)
	}

	if cfg.Debug {
		log.Println("aguardando todas as rotinas de monitoramento encerrarem...")
	}
	wg.Wait()
	if cfg.Debug {
		log.Println("todas as rotinas de monitoramento foram encerradas.")
	}
	return nil
}

func monitorUser(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) {
	if cfg.Debug {
		log.Printf("iniciando worker para o usuário: %s", u.Usuario)
	}

	switch u.Protocolo {
	case "I":
		email.MonitorarIMAP(ctx, u, cfg, conn)
	case "P":
		email.MonitorarPOP3(ctx, u, cfg, conn)
	default:
		if cfg.Debug {
			log.Printf("protocolo não reconhecido para %s. Encerrando worker.", u.Usuario)
		}
	}

	if cfg.Debug {
		log.Printf("Worker para o usuário %s foi encerrado graciosamente.", u.Usuario)
	}
}
