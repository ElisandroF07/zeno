package main

import "database/sql"

// settings é o key-value de singleton do Zeno: conta ACME, token do GitHub —
// qualquer config de instância única que não é preset, app nem serviço, e
// não vale a pena virar uma tabela própria só para uma linha.
func lerSetting(db *sql.DB, chave string) (valor string, existe bool, err error) {
	err = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, chave).Scan(&valor)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return valor, err == nil, err
}

func gravarSetting(db *sql.DB, chave, valor string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		chave, valor)
	return err
}

func apagarSetting(db *sql.DB, chave string) error {
	_, err := db.Exec(`DELETE FROM settings WHERE key = ?`, chave)
	return err
}
