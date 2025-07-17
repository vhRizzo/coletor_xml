package email

import (
	"coletor_xml/config"
	"coletor_xml/db"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const (
	ignoreFolder         string = "Lidos"
	connectionTimeout           = 30 * time.Second
	initialRetryInterval        = 10 * time.Second
	maxRetryInterval            = 5 * time.Minute
	sliceSize            int    = 25
)

var sep string

func MonitorarIMAP(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) {
	if cfg.Debug {
		log.Printf("iniciando monitoramento para %s", u.Usuario)
	}

	retryInterval := initialRetryInterval

	for {
		select {
		case <-ctx.Done():
			if cfg.Debug {
				log.Printf("encerrando monitoramento para %s: contexto cancelado", u.Usuario)
			}
		default:
			if err := start(ctx, u, cfg, conn); err != nil {
				if cfg.Debug {
					log.Printf("erro no monitoramento de %s: %v", u.Usuario, err)
				}
				if cfg.Debug {
					log.Printf("próxima tentativa em %v...", retryInterval)
				}

				time.Sleep(retryInterval)
				retryInterval = min(retryInterval*2, maxRetryInterval)

				continue
			}

			if cfg.Debug {
				log.Printf("[%s] ciclo de varredura concluído com sucesso.", u.Usuario)
			}
			retryInterval = initialRetryInterval

			// Mover o IDLE para cá.
		}
	}
}

func start(ctx context.Context, u db.UsuarioEmail, cfg config.Config, conn *sql.DB) error {
	endereco := fmt.Sprintf("%s:%d", u.Host, u.Porta)
	var c *client.Client
	var err error

	if cfg.Debug {
		log.Printf("[%s] iniciando conexão com %s", u.Usuario, endereco)
	}
	if strings.EqualFold(u.SSL, "S") {
		c, err = client.DialTLS(endereco, nil)
	} else {
		c, err = client.Dial(endereco)
	}
	if err != nil {
		return fmt.Errorf("falha ao iniciar a conexão: %w", err)
	}

	defer c.Logout()

	c.Timeout = connectionTimeout

	if cfg.Debug {
		log.Printf("conexão estabelecida com sucesso\n[%s] realizando o login", u.Usuario)
	}
	if err := c.Login(u.Usuario, u.Senha); err != nil {
		return fmt.Errorf("falha no login: %w", err)
	}
	if cfg.Debug {
		log.Println("login realizado com sucesso")
	}

	if err := createLidos(c, cfg); err != nil {
		return fmt.Errorf("erro ao preparar a pasta '%s'", ignoreFolder)
	}

	if cfg.Debug {
		log.Printf("[%s] iniciando varredura da caixa de entrada", u.Usuario)
	}

	inboxInfo := &imap.MailboxInfo{Name: "INBOX", Delimiter: sep}
	if err := processMailbox(c, inboxInfo, cfg, conn, u); err != nil {
		return fmt.Errorf("erro na varredura da INBOX: %w", err)
	}

	if cfg.Debug {
		log.Printf("[%s] varredura inicial concluída. Entrando em modo IDLE.", u.Usuario)
	}

	for {
		// Verificamos o contexto a cada iteração do loop.
		if ctx.Err() != nil {
			return context.Canceled
		}

		if cfg.Debug {
			log.Printf("[%s] entrando em modo IDLE, aguardando novas mensagens...", u.Usuario)
		}

		updates := make(chan client.Update, 1)
		c.Updates = updates

		stopIdle := make(chan struct{})
		idleDone := make(chan error, 1)
		go func() {
			idleDone <- c.Idle(stopIdle, nil)
		}()

		select {
		case update := <-updates:
			// Uma atualização chegou (ex: nova mensagem, mensagem apagada, etc.).
			if mboxUpdate, ok := update.(*client.MailboxUpdate); ok {
				if cfg.Debug {
					log.Printf("[%s] atualização recebida na caixa de correio: %v. Re-escaneando INBOX.", u.Usuario, mboxUpdate.Mailbox.Name)
				}
			} else {
				if cfg.Debug {
					log.Printf("[%s] atualização recebida (não é de caixa de correio): %T", u.Usuario, update)
				}
			}

			// Pare o IDLE para podermos escanear.
			close(stopIdle)
			<-idleDone // Espere o comando IDLE terminar.

			// Agora que recebemos uma atualização, fazemos uma varredura direcionada.
			// Não precisamos escanear todas as pastas de novo, apenas a INBOX.
			inboxInfo := &imap.MailboxInfo{Name: "INBOX", Delimiter: sep}
			if err := processMailbox(c, inboxInfo, cfg, conn, u); err != nil {
				// Se o scan pós-update falhar, a conexão provavelmente está ruim.
				// Retornamos o erro para o loop de resiliência principal.
				return fmt.Errorf("falha ao escanear INBOX após atualização: %w", err)
			}
			// Após o scan, o loop `for` continua e entraremos em IDLE novamente.

		case err := <-idleDone:
			// O comando IDLE terminou com um erro (ex: timeout do servidor).
			// Isso significa que a conexão morreu. Retornamos o erro para o loop de resiliência.
			if err != nil {
				return fmt.Errorf("conexão IDLE perdida: %w", err)
			}
			// Se o erro for nil, pode ter sido um stop gracioso.
			if cfg.Debug {
				log.Printf("[%s] conexão IDLE interrompida sem erros", u.Usuario)
			}
			// Podemos continuar o loop para re-entrar em IDLE.

		case <-ctx.Done():
			// O contexto da aplicação foi cancelado.
			if cfg.Debug {
				log.Printf("[%s] contexto cancelado, parando IDLE...", u.Usuario)
			}
			close(stopIdle)         // Tenta parar o IDLE graciosamente.
			<-idleDone              // Espera ele terminar.
			return context.Canceled // Retorna um erro específico para o contexto.
		}
	}
}

func createLidos(c *client.Client, cfg config.Config) error {
	delims := make(chan *imap.MailboxInfo, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.List("", "", delims)
	}()

	for m := range delims {
		sep = m.Delimiter
		break
	}
	if err := <-done; err != nil {
		return fmt.Errorf("erro ao identificar o delimitador: %w", err)
	}

	mboxName := "INBOX" + sep + ignoreFolder

	exists, err := checkMailboxExists(c, mboxName)
	if err != nil {
		return fmt.Errorf("falha ao verificar se a caixa de email [%s] já existe: %w", mboxName, err)
	}

	if !exists {
		if !cfg.Simulation {
			if err := c.Create(mboxName); err != nil {
				return fmt.Errorf("falha ao criar a caixa de email [%s]: %w", mboxName, err)
			}
		}

		if cfg.Debug {
			log.Printf("caixa de email [%s] criada com sucesso!", mboxName)
		}
	}
	if cfg.Debug {
		log.Println("caixa de email já existe, prosseguindo...")
	}

	return nil
}

func checkMailboxExists(c *client.Client, name string) (bool, error) {
	var exists bool

	mailboxes := make(chan *imap.MailboxInfo, 50)
	done := make(chan error, 1)

	go func() {
		done <- c.List("", "*", mailboxes)
	}()

	for m := range mailboxes {
		if strings.EqualFold(m.Name, name) {
			exists = true
			break
		}
	}

	if err := <-done; err != nil {
		return false, err
	}

	return exists, nil
}

func processMailbox(c *client.Client, mbox *imap.MailboxInfo, cfg config.Config, conn *sql.DB, u db.UsuarioEmail) error {
	if cfg.Debug {
		log.Printf("processando caixa de email [%s]", mbox.Name)
	}

	status, err := c.Select(mbox.Name, false)
	if err != nil {
		return fmt.Errorf("falha ao selecionar a caixa de email [%s]: %w", mbox.Name, err)
	}
	if status.Messages == 0 {
		if cfg.Debug {
			log.Printf("nenhuma mensagem na caixa [%s]", mbox.Name)
		}
		return nil
	}

	uids, err := c.UidSearch(imap.NewSearchCriteria())
	if err != nil {
		return fmt.Errorf("falha ao buscar os UIDs da caixa [%s]: %w", mbox.Name, err)
	}
	length := len(uids)
	if cfg.Debug {
		log.Printf("encontrados %d UIDs na caixa [%s].", length, mbox.Name)
	}

	for i := 0; i < length; i += sliceSize {
		end := min(i+sliceSize, length)

		batchUids := uids[i:end]
		if cfg.Debug {
			log.Printf("processando lote %d/%d (UIDs %d a %d)", (i/sliceSize)+1, (length/sliceSize)+1, batchUids[0], batchUids[len(batchUids)-1])
		}

		batchSet := new(imap.SeqSet)
		batchSet.AddNum(batchUids...)

		section := &imap.BodySectionName{}
		messages := make(chan *imap.Message, sliceSize)
		done := make(chan error, 1)

		go func() {
			done <- c.UidFetch(batchSet, []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}, messages)
		}()

		var uidsToMove []uint32
		var processingError bool

	processLoop:
		for {
			select {
			case msg, ok := <-messages:
				if !ok {
					break processLoop
				}
				if err := processMessage(msg, section, cfg, conn, u); err != nil {
					if cfg.Debug {
						log.Printf("falha ao processar UID %d: %v. Este lote será repetido se a conexão cair.", msg.Uid, err)
					}
					// Podemos decidir se queremos parar o lote inteiro ou apenas pular a mensagem.
					// Para simplificar, vamos parar o lote para forçar a repetição via loop de resiliência.
					processingError = true
				} else {
					uidsToMove = append(uidsToMove, msg.Uid)
				}
			case err := <-done:
				if err != nil {
					return fmt.Errorf("falha no UidFetch do lote: %w", err)
				}
			}
		}

		if processingError {
			return fmt.Errorf("erro de processamento encontrado no lote, a varredura será reiniciada")
		}

		// Mova as mensagens processadas com sucesso neste lote.
		if len(uidsToMove) > 0 {
			moveSet := new(imap.SeqSet)
			moveSet.AddNum(uidsToMove...)
			if err := c.UidMove(moveSet, "INBOX"+sep+ignoreFolder); err != nil {
				return fmt.Errorf("erro ao mover lote de UIDs: %w", err)
			}
			if cfg.Debug {
				log.Printf("lote de %d mensagens movido com sucesso.", len(uidsToMove))
			}
		}
	}

	if cfg.Debug {
		log.Printf("varredura em lotes da caixa [%s] concluída com sucesso.", mbox.Name)
	}
	return nil
}

// A função agora só processa e não move. Retorna um erro se algo falhar.
func processMessage(msg *imap.Message, section *imap.BodySectionName, cfg config.Config, conn *sql.DB, u db.UsuarioEmail) error {
	r := msg.GetBody(section)
	if r == nil {
		return nil // Não é um erro, apenas não há corpo para processar.
	}
	defer func() {
		if closer, ok := r.(io.Closer); ok {
			closer.Close()
		}
	}()

	m, err := mail.ReadMessage(r)
	if err != nil {
		return fmt.Errorf("falha ao ler a mensagem: %w", err)
	}

	date, err := m.Header.Date()
	if err != nil {
		if cfg.Debug {
			log.Printf("falha ao obter a data. Tratando como uma mensagem nova. %v", err)
		}
		date = time.Now()
	}

	// Se a mensagem for antiga, simplesmente a ignore.
	// Ela será adicionada à lista seqNumsToMove para ser movida.
	if date.Year() < 2025 {
		if cfg.Debug {
			log.Printf("mensagem antiga (%s) do SeqNum %d será movida para %s.", date, msg.SeqNum, ignoreFolder)
		}
		return nil // Retornar nil significa que o "processamento" (a decisão de mover) foi um sucesso.
	}

	if err := ProcessarMensagem(m, conn, cfg, u); err != nil {
		return fmt.Errorf("falha ao processar a mensagem: %w", err)
	}

	if cfg.Simulation {
		if cfg.Debug {
			log.Printf("[SIMULAÇÃO] mensagem %d processada com sucesso!", msg.SeqNum)
		}
	}

	return nil
}
