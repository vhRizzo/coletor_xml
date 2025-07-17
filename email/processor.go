package email

import (
	"bytes"
	"coletor_xml/config"
	"coletor_xml/db"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

// Max allowed XML size to prevent memory exhaustion
const (
	dbTimeout   = 30 * time.Second
	maxDataSize = 25 * 1024 * 1024 // 25MB
)

type xmlAnexo struct {
	filename string
	content  []byte
}

func ProcessarMensagem(msg *mail.Message, db *sql.DB, cfg config.Config, usuario db.UsuarioEmail) error {
	bodyBytes, err := io.ReadAll(io.LimitReader(msg.Body, maxDataSize))
	if err != nil {
		return fmt.Errorf("erro ao ler corpo da mensagem: %w", err)
	}

	header := msg.Header
	subject := header.Get("Subject")
	from := header.Get("From")
	if cfg.Debug {
		log.Printf("processando mensagem de [%s]", from)
	}

	date, err := header.Date()
	if err != nil {
		if cfg.Debug {
			log.Printf("[%s] Data inválida na mensagem: %v", usuario.Usuario, err)
		}
		date = time.Now()
	}

	if date.Year() < 2025 {
		if cfg.Debug {
			log.Printf("[%s] Mensagem antiga (%s). Processando arquivamento.", usuario.Usuario, date)
		}
		return handleOldMessage(usuario.Protocolo)
	}

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return nil // Not a multipart message, skip
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("falha ao iniciar a transação: %w", err)
	}
	defer tx.Rollback()

	var body string
	var anexosValidos []xmlAnexo
	mr := multipart.NewReader(bytes.NewReader(bodyBytes), params["boundary"])
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("erro ao ler parte da mensagem: %w", err)
		}

		filename := p.FileName()
		if filename != "" && strings.HasSuffix(strings.ToLower(filename), ".xml") {
			// É um anexo XML.
			xmlContent, err := io.ReadAll(io.LimitReader(p, maxDataSize))
			if err != nil {
				if cfg.Debug {
					log.Printf("[%s] erro ao ler anexo XML: %v", usuario.Usuario, err)
				}
				continue
			}

			// Remove espaços em branco do início/fim para uma detecção mais precisa.
			trimmedContent := bytes.TrimSpace(xmlContent)

			// Heurística: Se o conteúdo não começa com '<' (indicando XML) mas começa com
			// os caracteres de um XML em Base64, tentamos decodificar.
			if !bytes.HasPrefix(trimmedContent, []byte("<")) && len(trimmedContent) > 0 {
				if cfg.Debug {
					log.Printf("anexo [%s] parece estar em Base64. Tentando decodificar manualmente...", filename)
				}
				decodedBytes, err := base64.StdEncoding.DecodeString(string(xmlContent))
				if err != nil {
					// Se a decodificação falhar, pode não ser Base64 afinal.
					if cfg.Debug {
						log.Printf("falha ao decodificar manualmente o anexo [%s]: %v. Prosseguindo com o conteúdo original.", filename, err)
					}
					continue
				}
				// Se a decodificação for bem-sucedida, substitua o conteúdo original pelo decodificado.
				if cfg.Debug {
					log.Printf("anexo [%s] decodificado com sucesso.", filename)
				}
				xmlContent = decodedBytes
			}
			// Adiciona o XML válido à nossa lista para inserção posterior.
			anexosValidos = append(anexosValidos, xmlAnexo{filename: filename, content: xmlContent})
		} else if strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain") && filename == "" {
			// É a parte de texto do corpo do e-mail.
			if decodedBody, err := decodePartBody(p, cfg); err == nil {
				body = decodedBody
				if cfg.Debug {
					log.Println("corpo de texto da mensagem extraído e decodificado com sucesso.")
				}
				continue
			}
			if cfg.Debug {
				log.Printf("[%s] erro ao decodificar a parte de texto do corpo: %v", usuario.Usuario, err)
			}
		}
	}

	// Segunda Passagem: Inserir os anexos válidos que coletamos.
	if !cfg.Simulation {
		if len(anexosValidos) == 0 {
			if cfg.Debug {
				log.Println("Nenhum anexo XML válido encontrado ou processado nesta mensagem.")
			}
		}

		for _, anexo := range anexosValidos {
			if cfg.Debug {
				log.Printf("inserindo anexo válido no banco: %s", anexo.filename)
			}
			if err := inserirXML(tx, from, subject, date, body, string(anexo.content), usuario); err != nil {
				if cfg.Debug {
					log.Printf("[%s] Erro no pipeline de inserção do XML [%s]: %v", usuario.Usuario, anexo.filename, err)
				}
				// Se uma inserção falhar, o defer tx.Rollback() irá reverter a transação inteira.
				// Retornamos o erro para parar o processamento desta mensagem.
				return err
			}
			if cfg.Debug {
				log.Println("XML inserido com sucesso")
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("falha no commit da transação: %w", err)
		}
	}

	return nil
}

func handleOldMessage(protocol string) error {
	switch protocol {
	case "I":
		// The IMAP monitor will handle moving to "Lidos" folder
		return nil
	case "P":
		// The POP3 monitor will handle deletion
		return nil
	default:
		return fmt.Errorf("protocolo desconhecido: %s", protocol)
	}
}

func inserirXML(tx *sql.Tx, from, subject string, date time.Time, body, xml string, u db.UsuarioEmail) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	// Normalização a partir das constraints do banco.
	fromSafe := truncateString(from, 200)
	subjectSafe := truncateString(subject, 200)
	bodySafe := truncateString(body, 200)

	sqlScript := `
	INSERT INTO CPDXMLEM (
		CENVIXMLEM, CASSUXMLEM, DDTENXMLEM, CCORPXMLEM, CEXMLXMLEM, NNUMEGRUPO, NNUMEEMPRE
	) VALUES (:1, :2, :3, :4, :5, :6, :7)
	RETURNING NNUMEXMLEM INTO :8`

	var newID int64

	if _, err := tx.ExecContext(ctx, sqlScript,
		fromSafe, subjectSafe, date, bodySafe, xml, u.NumGrupo, u.NumEmpresa, &newID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, "BEGIN CPD_PGERA_XML_EMAIL_TMP(:1); END;", newID); err != nil {
		return err
	}

	return nil
}

func truncateString(s string, length int) string {
	runes := []rune(s)
	if len(runes) > length {
		return string(runes[:length])
	}
	return s
}

// decodePartBody lê o corpo de uma parte MIME, detecta seu charset a partir dos cabeçalhos e o converte para uma string UTF-8.
func decodePartBody(p *multipart.Part, cfg config.Config) (string, error) {
	// Pega o Content-Type para descobrir o charset
	mediaType, params, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
	if err != nil {
		// Se não conseguir parsear, lê como está (fallback)
		bodyBytes, err := io.ReadAll(io.LimitReader(p, maxDataSize))
		return string(bodyBytes), err
	}

	// Se não for texto, não tentamos decodificar.
	if !strings.HasPrefix(mediaType, "text/") {
		bodyBytes, err := io.ReadAll(io.LimitReader(p, maxDataSize))
		return string(bodyBytes), err
	}

	charset, ok := params["charset"]
	if !ok {
		charset = "utf-8" // Se o charset for omitido, o padrão é assumir UTF-8
	}

	// Procura o decodificador para o charset especificado.
	// A biblioteca ianaindex mapeia nomes de charset (ex: "iso-8859-1") para decodificadores.
	encoding, err := ianaindex.IANA.Encoding(charset)
	if err != nil {
		// Se não encontrarmos um decodificador, logamos o aviso e tentamos ler como está.
		if cfg.Debug {
			log.Printf("charset desconhecido ou não suportado: %s. Tentando ler como fallback.", charset)
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(p, maxDataSize))
		return string(bodyBytes), err
	}

	// Cria um leitor que transforma a codificação na hora da leitura.
	transformingReader := transform.NewReader(p, encoding.NewDecoder())

	// Lê do leitor transformador. Os bytes resultantes já estarão em UTF-8.
	utf8Bytes, err := io.ReadAll(io.LimitReader(transformingReader, maxDataSize))
	if err != nil {
		return "", fmt.Errorf("erro ao ler corpo com transformação de charset: %w", err)
	}

	return string(utf8Bytes), nil
}
