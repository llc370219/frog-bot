package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frogbot/internal/agy"
	"frogbot/internal/bot"
	"frogbot/internal/remote"
)

const runtimeFileName = "runtime.json"

type runtimeState struct {
	PID          int       `json:"pid"`
	Version      string    `json:"version"`
	Mode         string    `json:"mode"` // "local" | "server"
	Addr         string    `json:"addr"`
	URL          string    `json:"url"`
	DataDir      string    `json:"data_dir"`
	Workspace    string    `json:"workspace"`
	LogFile      string    `json:"log_file,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	ControlAddr  string    `json:"control_addr"`
	ControlToken string    `json:"control_token"`
}

type liveStatus struct {
	PID       int                `json:"pid"`
	Version   string             `json:"version"`
	Mode      string             `json:"mode"`
	Addr      string             `json:"addr"`
	URL       string             `json:"url"`
	DataDir   string             `json:"data_dir"`
	Workspace string             `json:"workspace"`
	LogFile   string             `json:"log_file,omitempty"`
	StartedAt time.Time          `json:"started_at"`
	BotCount  int                `json:"bot_count"`
	Remote    *remote.StatusView `json:"remote,omitempty"`
	Agents    []agy.AgentCLIView `json:"agents,omitempty"`
}

type localControlServer struct {
	srv      *http.Server
	state    runtimeState
	dataDir  string
	myPID    int
}

func startLocalControlServer(
	ctx context.Context,
	bots *bot.Bots,
	st runtimeState,
	onStop func(),
) (*localControlServer, error) {
	var rawTok [32]byte
	if _, err := rand.Read(rawTok[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(rawTok[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	st.PID = os.Getpid()
	st.StartedAt = time.Now()
	st.ControlAddr = ln.Addr().String()
	st.ControlToken = token

	authHandler := buildLocalControlHandler(bots, st, token, onStop)

	srv := &http.Server{
		Handler:           authHandler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		_ = srv.Serve(ln)
	}()

	if err := writeRuntimeState(st.DataDir, st); err != nil {
		_ = srv.Close()
		return nil, err
	}
	return &localControlServer{
		srv:     srv,
		state:   st,
		dataDir: st.DataDir,
		myPID:   st.PID,
	}, nil
}

func buildLocalControlHandler(bots *bot.Bots, st runtimeState, token string, onStop func()) http.Handler {
	mux := http.NewServeMux()
	writeCtlJSON := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			v = map[string]string{"error": err.Error()}
		}
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /ctl/status", func(w http.ResponseWriter, r *http.Request) {
		out := liveStatus{
			PID:       st.PID,
			Version:   st.Version,
			Mode:      st.Mode,
			Addr:      st.Addr,
			URL:       st.URL,
			DataDir:   st.DataDir,
			Workspace: st.Workspace,
			LogFile:   st.LogFile,
			StartedAt: st.StartedAt,
			BotCount:  bots.BotCount(),
			Agents:    agy.DiscoverAgents(),
		}
		if rm := bots.Remote(); rm != nil {
			rs := rm.Status()
			out.Remote = &rs
		}
		writeCtlJSON(w, out, nil)
	})

	mux.HandleFunc("POST /ctl/pair", func(w http.ResponseWriter, r *http.Request) {
		rm := bots.Remote()
		if rm == nil {
			writeCtlJSON(w, nil, errors.New("远程访问网关未初始化"))
			return
		}
		var body struct {
			AutoEnable bool `json:"auto_enable"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		cur := rm.Status()
		if cur.Mode == remote.ModeOff && body.AutoEnable {
			_, err := rm.UpdateConfig(remote.SaveInput{
				Mode:           remote.ModeE2EE,
				RealityListen:  cur.RealityListen,
				P2PListen:      cur.P2PListen,
				RelayAddr:      cur.RelayAddr,
				RelayToken:     cur.RelayToken,
				E2EERelayURL:   cur.E2EERelayURL,
				E2EERelayToken: cur.E2EERelayToken,
				E2EELANListen:  cur.E2EELANListen,
				PublicHost:     cur.PublicHost,
				SNI:            cur.SNI,
				ServerName:     cur.ServerName,
			})
			if err != nil {
				writeCtlJSON(w, nil, err)
				return
			}
		}
		pair, err := rm.CreatePair()
		writeCtlJSON(w, pair, err)
	})

	mux.HandleFunc("GET /ctl/devices", func(w http.ResponseWriter, r *http.Request) {
		rm := bots.Remote()
		if rm == nil {
			writeCtlJSON(w, []remote.PublicDevice{}, nil)
			return
		}
		writeCtlJSON(w, rm.Status().Devices, nil)
	})

	mux.HandleFunc("DELETE /ctl/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		rm := bots.Remote()
		if rm == nil {
			writeCtlJSON(w, nil, errors.New("远程访问网关未初始化"))
			return
		}
		writeCtlJSON(w, rm.RevokeDevice(r.PathValue("id")), nil)
	})

	mux.HandleFunc("POST /ctl/remote/config", func(w http.ResponseWriter, r *http.Request) {
		rm := bots.Remote()
		if rm == nil {
			writeCtlJSON(w, nil, errors.New("远程访问网关未初始化"))
			return
		}
		var in remote.SaveInput
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			writeCtlJSON(w, nil, errors.New("请求格式无效"))
			return
		}
		sv, err := rm.UpdateConfig(in)
		writeCtlJSON(w, sv, err)
	})

	mux.HandleFunc("POST /ctl/stop", func(w http.ResponseWriter, r *http.Request) {
		writeCtlJSON(w, map[string]bool{"ok": true}, nil)
		if onStop != nil {
			go func() {
				time.Sleep(40 * time.Millisecond)
				onStop()
			}()
		}
	})

	wantAuth := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if len(got) != len(wantAuth) || subtle.ConstantTimeCompare([]byte(got), []byte(wantAuth)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *localControlServer) Close() {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
	removeRuntimeStateIfOwned(c.dataDir, c.myPID)
}

func runtimeStatePath(dataDir string) string {
	return filepath.Join(dataDir, runtimeFileName)
}

func writeRuntimeState(dataDir string, st runtimeState) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, ".runtime-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, runtimeStatePath(dataDir))
}

func readRuntimeState(dataDir string) (runtimeState, error) {
	raw, err := os.ReadFile(runtimeStatePath(dataDir))
	if err != nil {
		return runtimeState{}, err
	}
	var st runtimeState
	if err := json.Unmarshal(raw, &st); err != nil {
		return runtimeState{}, err
	}
	return st, nil
}

func removeRuntimeStateIfOwned(dataDir string, pid int) {
	cur, err := readRuntimeState(dataDir)
	if err != nil || cur.PID == pid {
		_ = os.Remove(runtimeStatePath(dataDir))
	}
}

type controlClient struct {
	state runtimeState
	http  *http.Client
}

func connectLiveControl(dataDir string) (*controlClient, *liveStatus, error) {
	st, err := readRuntimeState(dataDir)
	if err != nil {
		return nil, nil, err
	}
	if st.ControlAddr == "" || st.ControlToken == "" {
		return nil, nil, errors.New("运行状态文件不完整")
	}
	cc := &controlClient{
		state: st,
		http:  &http.Client{Timeout: 8 * time.Second},
	}
	live, err := cc.Status()
	if err != nil {
		return nil, nil, err
	}
	return cc, live, nil
}

func (c *controlClient) doJSON(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+c.state.ControlAddr+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.state.ControlToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var er struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &er) == nil && strings.TrimSpace(er.Error) != "" {
			return errors.New(strings.TrimSpace(er.Error))
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *controlClient) Status() (*liveStatus, error) {
	var out liveStatus
	if err := c.doJSON(http.MethodGet, "/ctl/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *controlClient) Pair(autoEnable bool) (remote.PairResponse, error) {
	var out remote.PairResponse
	if err := c.doJSON(http.MethodPost, "/ctl/pair", map[string]bool{"auto_enable": autoEnable}, &out); err != nil {
		return remote.PairResponse{}, err
	}
	return out, nil
}

func (c *controlClient) Devices() ([]remote.PublicDevice, error) {
	var out []remote.PublicDevice
	if err := c.doJSON(http.MethodGet, "/ctl/devices", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlClient) RevokeDevice(id string) (remote.StatusView, error) {
	var out remote.StatusView
	if err := c.doJSON(http.MethodDelete, "/ctl/devices/"+id, nil, &out); err != nil {
		return remote.StatusView{}, err
	}
	return out, nil
}

func (c *controlClient) UpdateRemoteConfig(in remote.SaveInput) (remote.StatusView, error) {
	var out remote.StatusView
	if err := c.doJSON(http.MethodPost, "/ctl/remote/config", in, &out); err != nil {
		return remote.StatusView{}, err
	}
	return out, nil
}

func (c *controlClient) Stop() error {
	return c.doJSON(http.MethodPost, "/ctl/stop", nil, nil)
}
