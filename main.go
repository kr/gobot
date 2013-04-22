package main

import (
	"archive/tar"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
)

const (
	MaxTarSize = 2 * 1000 * 1000
)

var Q = make(chan *job)

var (
	version = capture("go", "version")
	distenv = capture("go", "tool", "dist", "env")
)

func main() {
	for i := 0; i < 5; i++ {
		go worker(i)
	}
	http.HandleFunc("/info", handleInfo)
	http.Handle("/build/", http.StripPrefix("/build/", http.HandlerFunc(handleBuild)))
	listen := ":" + os.Getenv("PORT")
	if listen == ":" {
		listen = ":8000"
	}
	err := http.ListenAndServe(listen, nil)
	if err != nil {
		panic(err)
	}
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Write(version)
	w.Write(distenv)
}

func handleBuild(w http.ResponseWriter, r *http.Request) {
	j := &job{
		pkg:  r.URL.Path,
		tar:  http.MaxBytesReader(w, r.Body, MaxTarSize),
		done: make(chan struct{}),
	}
	Q <- j
	<-j.done
	const httpTooLarge = "http: request body too large"
	if j.err != nil && j.err.Error() == httpTooLarge {
		http.Error(w, httpTooLarge, http.StatusRequestEntityTooLarge)
		return
	}
	if j.err != nil {
		log.Println(j.err)
		http.Error(w, "unprocessable entity", 422)
		w.Write(j.out)
		return
	}
	_, err := io.Copy(w, j.bin)
	if err != nil {
		log.Println(err)
		http.Error(w, "internal error", 500)
	}
}

func worker(n int) {
	gopath := "/tmp/" + strconv.Itoa(n)
	for j := range Q {
		if err := build(j, gopath); err != nil {
			j.err = err
		}
		j.done <- struct{}{}
	}
}

func build(j *job, gopath string) error {
	defer os.RemoveAll(gopath)
	err := untar(tar.NewReader(j.tar), gopath)
	if err != nil {
		return err
	}
	j.out, err = goget(gopath, j.pkg)
	if err != nil {
		return err
	}
	j.bin, err = os.Open(gopath + "/bin/" + path.Base(j.pkg))
	return err
}

func goget(gopath, pkg string) ([]byte, error) {
	cmd := exec.Command("go", "get", pkg)
	cmd.Env = append(os.Environ(), "GOPATH="+gopath)
	return cmd.CombinedOutput()
}

func untar(tr *tar.Reader, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if !strings.HasPrefix(hdr.Name, "src/") {
			return fmt.Errorf("bad path: %q", hdr.Name)
		}
		path := path.Join(dir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			if _, err = io.Copy(f, tr); err != nil {
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0777); err != nil {
				return err
			}
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			// ignore
		default:
			return fmt.Errorf("unsupported tar entry type %q", hdr.Typeflag)
		}
	}
	return nil
}

type job struct {
	tar    io.Reader
	pkg    string
	gopath string
	bin    io.Reader
	out    []byte
	err    error
	done   chan struct{}
}

func capture(name string, arg ...string) []byte {
	cmd := exec.Command(name, arg...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		panic(err)
	}
	return out
}
