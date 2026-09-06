package main

import "testing"

func TestNomeAppValido(t *testing.T) {
	validos := []string{"api", "meu-app", "app-2"}
	invalidos := []string{"", "a", "-x", "x-", "Api", "com espaço", "com_underscore"}

	for _, n := range validos {
		if !nomeAppValido.MatchString(n) {
			t.Errorf("%q deveria ser válido", n)
		}
	}
	for _, n := range invalidos {
		if nomeAppValido.MatchString(n) {
			t.Errorf("%q deveria ser inválido", n)
		}
	}
}

// Nome do container carrega o deployID — dois deploys da mesma app nunca
// colidem, e é isso que permite o novo subir ao lado do antigo.
func TestContainerDoDeployEhUnicoPorDeploy(t *testing.T) {
	a := containerDoDeploy("loja", 1)
	b := containerDoDeploy("loja", 2)
	if a == b {
		t.Fatalf("containers de deploys diferentes colidiram: %q", a)
	}
	if a != "zeno-app-loja-1" || b != "zeno-app-loja-2" {
		t.Errorf("formato inesperado: %q, %q", a, b)
	}
}

func TestLimparSSE(t *testing.T) {
	casos := map[string]string{
		"linha normal\n":     "linha normal",
		"com\r\nquebra\n":    "comquebra",
		"sem quebra nenhuma": "sem quebra nenhuma",
	}
	for entrada, quer := range casos {
		if got := limparSSE(entrada); got != quer {
			t.Errorf("limparSSE(%q) = %q, quer %q", entrada, got, quer)
		}
	}
}
