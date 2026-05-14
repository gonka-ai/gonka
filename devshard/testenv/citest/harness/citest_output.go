//go:build testenvci

package harness

import (
	"fmt"
	"os"
)

// CitestPhaseProgress prints a multi-line phase banner (I1, §7.7, I2a, I2b, I9) to stderr.
func CitestPhaseProgress(phase, docRef string, detail ...string) {
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\n\033[1;34m----------\033[0m \033[1;36mcitest:\033[0m \033[1;37m%s\033[0m \033[1;34m----------\033[0m\n", phase)
		_, _ = fmt.Fprintf(os.Stderr, "\033[35mDoc:\033[0m \033[2;35m%s\033[0m\n", docRef)
		for _, d := range detail {
			_, _ = fmt.Fprintf(os.Stderr, "\033[2m%s\033[0m\n", d)
		}
		_, _ = fmt.Fprintln(os.Stderr, "")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "")
	_, _ = fmt.Fprintf(os.Stderr, "---------- citest: %s ----------\n", phase)
	_, _ = fmt.Fprintf(os.Stderr, "Doc: %s\n", docRef)
	for _, d := range detail {
		_, _ = fmt.Fprintln(os.Stderr, d)
	}
	_, _ = fmt.Fprintln(os.Stderr, "")
}

// CitestPrintReuseStack notes TESTENV_REUSE_STACK=1 to stderr.
func CitestPrintReuseStack() {
	if StderrColorEnabled() {
		_, _ = fmt.Fprintln(os.Stderr, "\033[2;33mcitest:\033[0m \033[33mTESTENV_REUSE_STACK=1\033[0m \033[2m— skipping docker compose up/down; use ports 9100, 8428, 3000, 3100, devshardd metrics 19600+\033[0m")
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "citest: TESTENV_REUSE_STACK=1 — skipping docker compose up/down; use ports 9100, 8428, 3000, 3100, devshardd metrics 19600+")
}

// CitestPrintI1 prints a one-line I1 status to stderr.
func CitestPrintI1(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;36mcitest I1:\033[0m \033[97m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I1: %s\n", msg)
}

// CitestPrint77 prints a §7.7 observability status line to stderr.
func CitestPrint77(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;35mcitest §7.7:\033[0m \033[37m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest §7.7: %s\n", msg)
}

// CitestPrint77Wait prints a §7.7 poll retry line (dim).
func CitestPrint77Wait(err error) {
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[2;35mcitest §7.7 waiting:\033[0m \033[2m%v\033[0m\n", err)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest §7.7 waiting: %v\n", err)
}

// CitestPrintI2 prints a bracketed I2 line to stderr (intro / emphasis).
func CitestPrintI2(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;32m[citest I2]\033[0m \033[2;32m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[citest I2] %s\n", msg)
}

// CitestPrintI2Colon prints "citest I2: …" (steady-state success).
func CitestPrintI2Colon(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;32mcitest I2:\033[0m \033[37m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2: %s\n", msg)
}

// CitestPrintI2Wait prints a dim I2 poll / VM wait line.
func CitestPrintI2Wait(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[2;32mcitest I2\033[0m \033[2m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2 %s\n", msg)
}

// CitestPrintI2a prints a one-line I2a (per-host protocol /metrics scrape) status to stderr.
func CitestPrintI2a(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;36mcitest I2a:\033[0m \033[97m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2a: %s\n", msg)
}

// CitestPrintI2aWait prints a dim I2a retry line.
func CitestPrintI2aWait(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[2;36mcitest I2a\033[0m \033[2m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2a %s\n", msg)
}

// CitestPrintI2b prints an I2b (VictoriaMetrics) intro line to stderr.
func CitestPrintI2b(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;33m[citest I2b]\033[0m \033[2;33m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[citest I2b] %s\n", msg)
}

// CitestPrintI2bColon prints "citest I2b: …" (VM steady-state success).
func CitestPrintI2bColon(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[1;33mcitest I2b:\033[0m \033[37m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2b: %s\n", msg)
}

// CitestPrintI2bWait prints a dim I2b poll / VM wait line.
func CitestPrintI2bWait(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if StderrColorEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "\033[2;33mcitest I2b\033[0m \033[2m%s\033[0m\n", msg)
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "citest I2b %s\n", msg)
}
