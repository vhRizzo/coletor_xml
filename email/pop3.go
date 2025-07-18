package email

import (
	"coletor_xml/config"
	"coletor_xml/db"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/knadh/go-pop3"
)

// Constantes agrupadas para clareza.
const (
	maxRetryDelay     = 5 * time.Minute  // Intervalo máximo na ocorrência de erros.
	initialRetryDelay = 10 * time.Second // Intervalo inicial na ocorrência de erros.
	pollInterval      = 1 * time.Minute  // Intervalo padrão entre as checagens.
	operationTimeout  = 45 * time.Second // Tempo limite de resposta de operações.
)

func MonitorarPOP3(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) {
	// O Ticker controla o intervalo de polling.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Executa uma vez imediatamente no início, sem esperar o primeiro tick.
	runPOP3PollingCycle(ctx, u, cfg, conn)

	for {
		select {
		case <-ctx.Done():
			if cfg.Debug {
				log.Printf("[%s] encerrando monitoramento POP3 devido ao cancelamento do contexto.", u.Usuario)
			}
			return
		case <-ticker.C:
			// A cada tick do relógio, executamos um ciclo de polling.
			runPOP3PollingCycle(ctx, u, cfg, conn)
		}
	}
}

// runPOP3PollingCycle executa uma tentativa completa de conectar, processar e desconectar.
// Ele gerencia seus próprios retries internos para falhas de conexão.
func runPOP3PollingCycle(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) {
	retryDelay := initialRetryDelay

	for {
		// Verificamos o contexto antes de cada tentativa.
		if ctx.Err() != nil {
			return
		}

		err := processAllPOP3Messages(ctx, u, cfg, conn)
		if err == nil {
			return // Ciclo bem-sucedido, sai da função e aguarda o próximo tick.
		}

		// Se o erro for irrecuperável, logamos e paramos de tentar para este usuário.
		if shouldStop(err) {
			if cfg.Debug {
				log.Printf("[%s] erro irrecuperável: %v", u.Usuario, err)
			}
		}

		if cfg.Debug {
			log.Printf("[%s] erro no ciclo POP3: %v. Próxima tentativa em %v", u.Usuario, err, retryDelay)
		}

		// Espera antes de tentar novamente, respeitando o cancelamento.
		select {
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, maxRetryDelay)
		case <-ctx.Done():
			return
		}
	}
}

// processAllPOP3Messages conecta, lista, processa e deleta todas as mensagens em uma única sessão.
func processAllPOP3Messages(ctx context.Context, u db.UsuarioEmail, cfg config.Config, dbConn *sql.DB) error {
	// O cliente é leve, pode ser criado a cada ciclo.
	p := pop3.New(pop3.Opt{
		Host:        u.Host,
		Port:        u.Porta,
		TLSEnabled:  strings.ToUpper(u.SSL) == "S",
		DialTimeout: operationTimeout,
	})

	conn, err := p.NewConn()
	if err != nil {
		return fmt.Errorf("falha na conexão POP3: %w", err)
	}
	defer conn.Quit()

	if err := conn.Auth(u.Usuario, u.Senha); err != nil {
		return fmt.Errorf("falha na autenticação POP3: %w", err)
	}

	msgs, err := conn.List(0)
	if err != nil {
		return fmt.Errorf("falha ao listar mensagens POP3: %w", err)
	}

	if len(msgs) == 0 {
		return nil
	}

	var lastErr error
	for _, msg := range msgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Processa cada mensagem.
		rawMsg, err := conn.RetrRaw(msg.ID)
		if err != nil {
			if cfg.Debug {
				log.Printf("[%s] erro ao obter mensagem POP3 %d: %v", u.Usuario, msg.ID, err)
			}
			lastErr = err // Guarda o último erro, mas continua tentando outras mensagens.
			continue
		}

		m, err := mail.ReadMessage(rawMsg)
		if err != nil {
			if cfg.Debug {
				log.Printf("[%s] erro ao parsear mensagem POP3 %d: %v", u.Usuario, msg.ID, err)
			}
			lastErr = err
			continue
		}

		// Reutiliza a mesma lógica de processamento do IMAP.
		if err := ProcessarMensagem(m, dbConn, cfg, u); err != nil {
			if cfg.Debug {
				log.Printf("[%s] erro ao processar conteúdo da mensagem POP3 %d: %v", u.Usuario, msg.ID, err)
			}
			lastErr = err
			continue // Pula para a próxima mensagem, não deleta a que falhou.
		}

		// Deleta a mensagem do servidor se o processamento foi bem-sucedido.
		if !cfg.Simulation {
			if err := conn.Dele(msg.ID); err != nil {
				if cfg.Debug {
					log.Printf("[%s] erro ao deletar mensagem POP3 %d: %v", u.Usuario, msg.ID, err)
				}
				lastErr = err
			}
		} else {
			if cfg.Debug {
				log.Printf("[SIMULAÇÃO] [%s] Mensagem %d seria deletada", u.Usuario, msg.ID)
			}
		}
	}

	return lastErr // Retorna o último erro ocorrido, ou nil se tudo correu bem.
}

// shouldStop determina se um erro é crítico e o retry não deve continuar.
func shouldStop(err error) bool {
	// Erros de autenticação são bons candidatos para parar as tentativas.
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "authentication failed") ||
		strings.Contains(errText, "credentials rejected") ||
		strings.Contains(errText, "falha na autenticação")
}
