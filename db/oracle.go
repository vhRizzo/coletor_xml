package db

import (
	"coletor_xml/config"
	"log"

	"database/sql"

	go_ora "github.com/sijms/go-ora/v2"
)

type UsuarioEmail struct {
	Host       string
	Porta      int
	Usuario    string
	Senha      string
	SSL        string
	Metodo     *string
	Modo       *string
	Protocolo  string
	NumGrupo   int
	NumEmpresa int
}

func ConnectOracle(cfg config.Config) (*sql.DB, error) {
	urlOptions := map[string]string{
		"SID": cfg.DBService,
	}
	url := go_ora.BuildUrl(cfg.DBHost, cfg.DBPort, "", cfg.DBUser, cfg.DBPassword, urlOptions)

	if cfg.Debug {
		log.Printf("url montada com os dados salvos: %s", url)
	}
	return sql.Open("oracle", url)
}

func ListarUsuarios(db *sql.DB) ([]UsuarioEmail, error) {
	query := `
	SELECT NNUMEGRUPO, NNUMEEMPRE, CHOSTCEXML, NPORTCEXML, CUSERCEXML, CSENHCEXML, CUSSLCEXML, CMETOCEXML, CMODECEXML, CTPROCEXML
	FROM CPDCEXML`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var usuarios []UsuarioEmail
	for rows.Next() {
		var u UsuarioEmail
		err = rows.Scan(&u.NumGrupo, &u.NumEmpresa, &u.Host, &u.Porta, &u.Usuario, &u.Senha, &u.SSL, &u.Metodo, &u.Modo, &u.Protocolo)
		if err != nil {
			return nil, err
		}
		usuarios = append(usuarios, u)
	}
	return usuarios, nil
}
