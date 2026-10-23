// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Команда checkconfig — проба чарта: конфигурация оператора, которую
// отрисовал чарт, проходит тот же строгий разбор и fail-closed проверку,
// что и при старте оператора.
package main

import (
	"fmt"
	"os"

	"github.com/PRO-Robotech/registry-operator/internal/config"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkconfig <config.yaml>")
		os.Exit(2)
	}
	if _, err := config.Load(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "checkconfig:", err)
		os.Exit(1)
	}
	fmt.Println("checkconfig: configuration accepted")
}
