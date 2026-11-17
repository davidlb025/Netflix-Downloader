package main

import (
		netflixlib "local.netflix/netflix-downloader"
"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// This executable validates a newly written native host with a public DRM test.
// It validates the native host in isolation from the main application.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	helper := flag.String("helper", "assets/cdm-portable.exe", "Host portátil independiente")
	cdm := flag.String("cdm", "", "Ruta al CDM oficial ya instalado")
	pssh := flag.String("pssh", "research/public-test-pssh.bin", "PSSH del contenido público de prueba")
	licenseURL := flag.String("license", "https://proxy.uat.widevine.com/proxy", "Servidor de prueba Widevine UAT")
	provider := flag.String("provider", "uat", "Proveedor de licencia: uat o netflix")
	session := flag.String("session", "", "Archivo DPAPI local, solo para la prueba Netflix")
	viewableID := flag.Int("id", 80077368, "ID de reproducción para la prueba Netflix")
	hostPlatform := flag.String("host", "linux", "Plataforma real del CDM: windows o linux")
	decryptMedia := flag.String("decrypt-media", "", "Carpeta de las dos pistas cifradas del ensayo")
	clearOutput := flag.String("clear-out", "research/clear-private", "Carpeta nueva para las pistas descifradas")
	sampleCheck := flag.Int("sample-check", -1, "Diagnóstico aislado de una muestra, sin generar MP4")
	repeatSample := flag.Int("repeat-sample", 1, "Repeticiones del diagnóstico aislado (1 a 10)")
	decodeCheck := flag.Bool("decode-check", false, "Comprobar 20 muestras con el decodificador interno, sin generar MP4")
	bindManifest := flag.Bool("bind-manifest", false, "Completar el manifiesto licenciado con el desafío y sesión reales del CDM")
	renderVideo := flag.String("render-video", "", "Exportar los fotogramas decodificados y el audio a un MP4 nuevo")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "Ruta al ejecutable FFmpeg")
	controlled := flag.Bool("controlled", false, "Cancelar al cerrar la entrada del proceso controlador")
	audioLanguage := flag.String("audio-language", "", "Idioma BCP 47 de la pista de audio seleccionada")
	flag.Parse()
	if *cdm == "" {
		return errors.New("indica -cdm con la ruta a un CDM oficial")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	if *controlled {
		go func() { io.Copy(io.Discard, os.Stdin); cancel() }()
	}
	privateDir, err := os.MkdirTemp("", "netflix-cdm-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(privateDir)
	challengePath := filepath.Join(privateDir, "challenge.bin")
	responsePath := filepath.Join(privateDir, "response.bin")
	var plans []*netflixlib.Plan
	jobPath := filepath.Join(privateDir, "job.bin")
	resultPath := filepath.Join(privateDir, "decrypted.bin")
	var audioPath string
	if *decryptMedia != "" {
		for index := 0; index < 2; index++ {
			path := filepath.Join(*decryptMedia, fmt.Sprintf("track-%d.enc.mp4", index))
			if index > 0 {
				if _, err := os.Stat(path); os.IsNotExist(err) {
					break
				}
			}
			plan, err := netflixlib.CENCParse(path)
			if err != nil {
				return fmt.Errorf("pista %d: %w", index, err)
			}
			plans = append(plans, plan)
			fmt.Printf("{\"event\":\"cencPlan\",\"track\":%d,\"samples\":%d,\"patches\":%d}\n", index, len(plan.Samples), len(plan.Patches))
		}
		if *sampleCheck >= 0 {
			if *repeatSample < 1 || *repeatSample > 10 || len(plans) == 0 || *sampleCheck >= len(plans[0].Samples) {
				return errors.New("muestra de diagnóstico fuera de rango")
			}
			copyPlan := *plans[0]
			sample := copyPlan.Samples[*sampleCheck]
			copyPlan.Samples = nil
			for i := 0; i < *repeatSample; i++ {
				copyPlan.Samples = append(copyPlan.Samples, sample)
			}
			plans = []*netflixlib.Plan{&copyPlan}
		}
		if *renderVideo != "" {
			if *decodeCheck || *sampleCheck >= 0 || len(plans) == 0 || len(plans[0].VideoSamples) == 0 {
				return errors.New("opciones de exportación incompatibles")
			}
			if _, err := os.Stat(*renderVideo); err == nil {
				return errors.New("el archivo de destino ya existe")
			} else if !os.IsNotExist(err) {
				return err
			}
			if len(plans) > 1 {
				if len(plans[1].Samples) != 0 {
					return errors.New("la exportación del audio cifrado aún no está implementada")
				}
				audioPath = plans[1].Path
			}
			copyPlan := *plans[0]
			copyPlan.Samples = copyPlan.VideoSamples
			plans = []*netflixlib.Plan{&copyPlan}
		}
		if *decodeCheck {
			if *sampleCheck >= 0 || len(plans) == 0 || len(plans[0].Samples) == 0 {
				return errors.New("diagnóstico de decodificación no válido")
			}
			copyPlan := *plans[0]
			copyPlan.Samples = copyPlan.VideoSamples
			firstEncrypted := -1
			for i, sample := range copyPlan.Samples {
				if sample.Scheme != 0 {
					firstEncrypted = i
					break
				}
			}
			if firstEncrypted < 0 {
				return errors.New("la prueba no contiene vídeo cifrado")
			}
			if end := firstEncrypted + 20; end < len(copyPlan.Samples) {
				copyPlan.Samples = copyPlan.Samples[:end]
			}
			plans = []*netflixlib.Plan{&copyPlan}
		}
		if err = netflixlib.WriteJobMode(jobPath, plans, *decodeCheck || *renderVideo != ""); err != nil {
			return err
		}
	}
	var netflixClient *netflixlib.Client
	var playback *netflixlib.Playback
	var certificatePath string
	if *provider == "netflix" {
		cookies, err := netflixlib.ReadSession(*session)
		if err != nil {
			return err
		}
		netflixClient, err = netflixlib.NewForPlatform(ctx, cookies, *hostPlatform)
		if err != nil {
			return err
		}
		playback, err = netflixClient.Manifest(ctx, *viewableID)
		if err != nil {
			return netflixClient.Redact(err)
		}
		manifestStatus, _ := json.Marshal(map[string]any{"event": "netflixManifest", "heights": playback.Heights, "psshBytes": len(playback.PSSH), "publicPlayerSources": netflixClient.PlayerSources()})
		fmt.Println(string(manifestStatus))
		if len(plans) > 0 {
			selectedPSSH := netflixlib.WidevinePSSH(plans)
			if len(selectedPSSH) > 0 {
				fmt.Printf("{\"event\":\"selectedPSSH\",\"matchesManifest\":%t}\n", bytes.Equal(selectedPSSH, playback.PSSH))
				playback.PSSH = selectedPSSH
			}
		}
		psshPath := filepath.Join(privateDir, "pssh.bin")
		if err = os.WriteFile(psshPath, playback.PSSH, 0600); err != nil {
			return err
		}
		*pssh = psshPath
		certificate, err := netflixClient.Certificate(ctx, playback)
		if err != nil {
			return netflixClient.Redact(err)
		}
		certificatePath = filepath.Join(privateDir, "certificate.bin")
		if err = os.WriteFile(certificatePath, certificate, 0600); err != nil {
			return err
		}
		fmt.Printf("{\"event\":\"privacyCertificate\",\"bytes\":%d}\n", len(certificate))
	} else if *provider != "uat" {
		return errors.New("provider debe ser uat o netflix")
	}
	arguments := []string{*cdm, *pssh, challengePath, responsePath}
	if certificatePath != "" {
		arguments = append(arguments, certificatePath)
	} else if len(plans) > 0 {
		arguments = append(arguments, "")
	}
	if len(plans) > 0 {
		arguments = append(arguments, jobPath, resultPath)
	}
	if *renderVideo != "" {
		if len(plans) == 0 {
			return errors.New("la exportación necesita -decrypt-media")
		}
		arguments = append(arguments, "--y4m-stdout")
	}
	nativeExecutable := *helper
	if *hostPlatform != "linux" && *hostPlatform != "windows" {
		return errors.New("host debe ser windows o linux")
	}
	command := exec.CommandContext(ctx, nativeExecutable, arguments...)
	command.Stderr = os.Stderr
	var stdout io.ReadCloser
	var encoder *exec.Cmd
	var encoderInput io.WriteCloser
	var encoderErrors bytes.Buffer
	encodedPath := filepath.Join(privateDir, "render.mp4")
	if *renderVideo != "" {
		encoderArgs := []string{"-hide_banner", "-v", "error", "-xerror", "-nostdin", "-n", "-f", "yuv4mpegpipe", "-i", "pipe:0"}
		if audioPath != "" {
			encoderArgs = append(encoderArgs, "-i", audioPath)
		}
		encoderArgs = append(encoderArgs, "-map", "0:v:0")
		if audioPath != "" {
			encoderArgs = append(encoderArgs, "-map", "1:a:0")
		}
		encoderArgs = append(encoderArgs, "-c:v", "libx264", "-preset", "fast", "-crf", "18", "-c:a", "copy", "-movflags", "+faststart", encodedPath)
		if audioPath != "" {
			encoderArgs = append(encoderArgs[:len(encoderArgs)-1], "-metadata:s:a:0", "language="+netflixlib.AudioLanguageTag(*audioLanguage), encodedPath)
		}
		encoder = exec.CommandContext(ctx, *ffmpegPath, encoderArgs...)
		encoder.Stderr = &encoderErrors
		encoderInput, err = encoder.StdinPipe()
		if err != nil {
			return err
		}
		if err = encoder.Start(); err != nil {
			encoderInput.Close()
			return err
		}
		defer func() {
			encoderInput.Close()
			if encoder.ProcessState == nil {
				encoder.Process.Kill()
				encoder.Wait()
			}
		}()
		command.Stdout = encoderInput
		command.Stderr = nil
		stdout, err = command.StderrPipe()
	} else {
		stdout, err = command.StdoutPipe()
	}
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	client := &http.Client{Timeout: 25 * time.Second}
	var exchangeError error
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var event struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		fmt.Println(scanner.Text())
		if event.Event != "challengeReady" {
			continue
		}
		challenge, err := os.ReadFile(challengePath)
		if err != nil {
			exchangeError = err
			cancel()
			break
		}
		var body []byte
		if *provider == "netflix" {
			sessionID, err := os.ReadFile(challengePath + ".session")
			if err == nil {
				if *bindManifest {
					body, err = netflixClient.BindChallenge(ctx, playback, challenge, string(sessionID))
					if err == nil {
						fmt.Println("{\"event\":\"licensedManifestBound\"}")
					}
				}
				if err == nil && len(body) == 0 {
					body, err = netflixClient.License(ctx, playback, challenge, string(sessionID))
				}
			}
			if err != nil {
				exchangeError = netflixClient.Redact(err)
				cancel()
				break
			}
			fmt.Printf("{\"event\":\"netflixLicense\",\"bytes\":%d}\n", len(body))
			if policy, err := netflixlib.LicenseRequirements(body); err == nil {
				status, _ := json.Marshal(map[string]any{"event": "licenseRequirements", "policy": policy})
				fmt.Println(string(status))
			}
		} else {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, *licenseURL, bytes.NewReader(challenge))
			if err != nil {
				exchangeError = err
				cancel()
				break
			}
			request.Header.Set("Content-Type", "application/octet-stream")
			response, err := client.Do(request)
			if err != nil {
				exchangeError = err
				cancel()
				break
			}
			body, err = io.ReadAll(io.LimitReader(response.Body, 16<<20))
			response.Body.Close()
			fmt.Printf("{\"event\":\"licenseHTTP\",\"status\":%d,\"bytes\":%d}\n", response.StatusCode, len(body))
			if err != nil || response.StatusCode != http.StatusOK {
				exchangeError = fmt.Errorf("el servidor de prueba rechazó la solicitud: HTTP %d", response.StatusCode)
				cancel()
				break
			}
		}
		pending := responsePath + ".pending"
		if err = os.WriteFile(pending, body, 0600); err == nil {
			err = os.Rename(pending, responsePath)
		}
		if err != nil {
			exchangeError = err
			cancel()
			break
		}
	}
	waitError := command.Wait()
	if encoder != nil {
		encoderInput.Close()
		encoderError := encoder.Wait()
		if encoderError != nil {
			return fmt.Errorf("FFmpeg: %w: %.2000s", encoderError, encoderErrors.String())
		}
	}
	if exchangeError != nil {
		return exchangeError
	}
	if scanner.Err() != nil {
		return scanner.Err()
	}
	if waitError != nil {
		return fmt.Errorf("el host no obtuvo una licencia utilizable: %w", waitError)
	}
	if *renderVideo != "" {
		if err := publishNew(encodedPath, *renderVideo); err != nil {
			return err
		}
		info, err := os.Stat(*renderVideo)
		if err != nil {
			return err
		}
		fmt.Printf("{\"event\":\"videoExported\",\"bytes\":%d}\n", info.Size())
	}
	if len(plans) > 0 && *sampleCheck < 0 && !*decodeCheck && *renderVideo == "" {
		paths, err := netflixlib.ApplyResult(resultPath, plans, *clearOutput)
		if err != nil {
			return err
		}
		fmt.Printf("{\"event\":\"clearTracksWritten\",\"count\":%d}\n", len(paths))
	}
	if *sampleCheck >= 0 {
		info, err := os.Stat(resultPath)
		if err != nil {
			return err
		}
		expectedSize := int64(12)
		for _, sample := range plans[0].Samples {
			expectedSize += 16 + int64(sample.Size)
		}
		if info.Size() != expectedSize {
			return errors.New("resultado de muestra aislada incompleto")
		}
		fmt.Printf("{\"event\":\"isolatedSampleCheck\",\"sample\":%d,\"repeats\":%d,\"success\":true}\n", *sampleCheck, *repeatSample)
	}
	return nil
}

func publishNew(source, destination string) (result error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		output.Close()
		if result != nil {
			os.Remove(destination)
		}
	}()
	if _, err = io.Copy(output, input); err != nil {
		return err
	}
	if err = output.Sync(); err != nil {
		return err
	}
	return output.Close()
}
