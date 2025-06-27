package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/blang/semver"
	"github.com/kardianos/osext"
	gselfupdate "github.com/rhysd/go-github-selfupdate/selfupdate"
)

func CheckAndUpdate(version, repo string) {
	latest, found, err := gselfupdate.DetectLatest(repo)
	if err != nil {
		fmt.Println("Ошибка проверки обновлений:", err)
		return
	}
	v, err := semver.ParseTolerant(version)
	if err != nil {
		fmt.Println("Ошибка парсинга версии:", err)
		return
	}
	if found && latest.Version.GT(v) {
		fmt.Println("Найдена новая версия:", latest.Version)
		err = updateAndRestart(latest.Version, repo)
		if err != nil {
			fmt.Println("Ошибка обновления:", err)
		}
	}
}

func updateAndRestart(version semver.Version, repo string) error {
	_, err := gselfupdate.UpdateSelf(version, repo)
	if err != nil {
		return err
	}
	fmt.Println("Обновление успешно. Перезапуск...")
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func StartAutoUpdate(version, repo string, interval time.Duration) {
	fmt.Println("Текущая версия:", version, "Автоматическое обновление включено. Проверка каждые", interval)
	go func() {
		for {
			CheckAndUpdate(version, repo)
			time.Sleep(interval)
		}
	}()
}

// Restart app
func Restart() error {
	file, error := osext.Executable()
	if error != nil {
		return error
	}

	error = syscall.Exec(file, os.Args, os.Environ())
	if error != nil {
		return error
	}

	return nil
}
