package netflix

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type document = map[string]any

func encode(value any) []byte { data, _ := json.Marshal(value); return data }
func b64(data []byte) string  { return base64.StdEncoding.EncodeToString(data) }
func unb64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := encoding.DecodeString(value); err == nil {
			return data, nil
		}
	}
	return nil, errors.New("datos base64 no válidos")
}
func mslObject(value any) document { result, _ := value.(map[string]any); return result }
func text(value any) string     { result, _ := value.(string); return result }

type msl struct {
	client              *Client
	master              document
	encryption, signing []byte
	sequence            int64
	messageID           int64
}

func (m *msl) header(handshake bool) document {
	m.messageID = time.Now().UnixNano() & ((1 << 52) - 1)
	header := document{"messageid": m.messageID, "renewable": true, "handshake": handshake, "nonreplayable": false, "timestamp": time.Now().Unix(), "capabilities": document{"languages": []string{"es-ES"}, "compressionalgos": []string{"GZIP"}, "encoderformats": []string{"JSON"}}}
	if !handshake {
		header["sender"] = m.client.esn
		header["userauthdata"] = document{"scheme": "NETFLIXID", "authdata": document{"netflixid": m.client.cookies["NetflixId"], "securenetflixid": m.client.cookies["SecureNetflixId"]}}
	}
	return header
}

func (m *msl) post(ctx context.Context, endpoint string, body []byte) ([]document, error) {
	address, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := address.Query()
	requestName := "license"
	if strings.Contains(endpoint, "licensedmanifest") {
		requestName = "prefetch/licensedManifest"
	}
	for key, value := range map[string]string{"reqAttempt": "1", "reqName": requestName, "clienttype": "akira", "uiversion": m.client.build, "browsername": "chrome", "browserversion": "156.0.8078.73", "osname": m.client.osName, "osversion": m.client.osVersion} {
		query.Set(key, value)
	}
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	m.client.headers(request)
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.http.Do(request)
	if err != nil {
		return nil, errors.New("falló la conexión MSL con Netflix")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Netflix MSL: HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 48<<20))
	var chunks []document
	for {
		var chunk document
		if err := decoder.Decode(&chunk); err == io.EOF {
			break
		} else if err != nil {
			return nil, errors.New("respuesta MSL no válida")
		}
		if errorData := text(chunk["errordata"]); errorData != "" {
			data, _ := unb64(errorData)
			var detail document
			json.Unmarshal(data, &detail)
			return nil, fmt.Errorf("Netflix rechazó el sobre MSL (código %v, interno %v)", detail["errorcode"], detail["internalcode"])
		}
		chunks = append(chunks, chunk)
		if len(chunks) > 1000 {
			return nil, errors.New("demasiados fragmentos MSL")
		}
	}
	return chunks, nil
}

func (m *msl) handshake(ctx context.Context, endpoint string) error {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	public, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		return err
	}
	header := m.header(true)
	header["keyrequestdata"] = []document{{"scheme": "ASYMMETRIC_WRAPPED", "keydata": document{"keypairid": "netflix-downloader-rsa", "mechanism": "JWK_RSA", "publickey": b64(public)}}}
	first := document{"entityauthdata": document{"scheme": "NONE", "authdata": document{"identity": m.client.esn}}, "headerdata": b64(encode(header)), "signature": ""}
	last := document{"payload": b64(encode(document{"messageid": m.messageID, "data": "", "sequencenumber": 1, "endofmsg": true})), "signature": ""}
	chunks, err := m.post(ctx, endpoint, append(encode(first), encode(last)...))
	if err != nil {
		return fmt.Errorf("intercambio RSA: %w", err)
	}
	if len(chunks) == 0 {
		return errors.New("sin respuesta al intercambio MSL")
	}
	headerData, err := unb64(text(chunks[0]["headerdata"]))
	if err != nil {
		return err
	}
	var answer document
	if json.Unmarshal(headerData, &answer) != nil {
		return errors.New("cabecera de intercambio MSL no válida")
	}
	keyResponse := mslObject(answer["keyresponsedata"])
	if text(keyResponse["scheme"]) != "ASYMMETRIC_WRAPPED" {
		return errors.New("Netflix no aceptó el intercambio RSA MSL")
	}
	keyData := mslObject(keyResponse["keydata"])
	unwrap := func(name string) ([]byte, error) {
		wrapped, e := unb64(text(keyData[name]))
		if e != nil {
			return nil, e
		}
		plain, e := rsa.DecryptOAEP(sha1.New(), rand.Reader, private, wrapped, nil)
		if e != nil {
			return nil, errors.New("no se pudo verificar el intercambio MSL")
		}
		var jwk document
		if json.Unmarshal(plain, &jwk) != nil {
			return nil, errors.New("JWK MSL no válido")
		}
		return unb64(text(jwk["k"]))
	}
	if m.encryption, err = unwrap("encryptionkey"); err != nil {
		return err
	}
	if m.signing, err = unwrap("hmackey"); err != nil {
		return err
	}
	if len(m.encryption) != 16 || len(m.signing) != 32 {
		return errors.New("longitudes de claves MSL no válidas")
	}
	m.master = mslObject(keyResponse["mastertoken"])
	tokenBytes, _ := unb64(text(m.master["tokendata"]))
	var token document
	json.Unmarshal(tokenBytes, &token)
	if sequence, ok := token["sequencenumber"].(float64); ok {
		m.sequence = int64(sequence)
	} else {
		return errors.New("master token sin secuencia MSL")
	}
	return nil
}

func (m *msl) seal(value any) (string, string, error) {
	plain := encode(value)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", "", err
	}
	block, err := aes.NewCipher(m.encryption)
	if err != nil {
		return "", "", err
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
	envelope := encode(document{"keyid": m.client.esn + "_" + strconv.FormatInt(m.sequence, 10), "iv": b64(iv), "ciphertext": b64(encrypted), "sha256": "AA=="})
	mac := hmac.New(sha256.New, m.signing)
	mac.Write(envelope)
	return b64(envelope), b64(mac.Sum(nil)), nil
}

func (m *msl) open(encoded, signature string) (document, error) {
	envelope, err := unb64(encoded)
	if err != nil {
		return nil, err
	}
	provided, err := unb64(signature)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, m.signing)
	mac.Write(envelope)
	if !hmac.Equal(mac.Sum(nil), provided) {
		return nil, errors.New("firma MSL incorrecta")
	}
	var fields document
	if json.Unmarshal(envelope, &fields) != nil {
		return nil, errors.New("sobre MSL no válido")
	}
	iv, err := unb64(text(fields["iv"]))
	if err != nil || len(iv) != aes.BlockSize {
		return nil, errors.New("IV MSL no válido")
	}
	encrypted, err := unb64(text(fields["ciphertext"]))
	if err != nil || len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, errors.New("bloques MSL no válidos")
	}
	block, err := aes.NewCipher(m.encryption)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, encrypted)
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plain) || !bytes.Equal(plain[len(plain)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("relleno MSL no válido")
	}
	var result document
	if json.Unmarshal(plain[:len(plain)-padding], &result) != nil {
		return nil, errors.New("contenido MSL no válido")
	}
	return result, nil
}

func (m *msl) request(ctx context.Context, endpoint string, value any) (any, error) {
	if m.master == nil {
		if err := m.handshake(ctx, endpoint); err != nil {
			return nil, err
		}
	}
	header, signature, err := m.seal(m.header(false))
	if err != nil {
		return nil, err
	}
	var zipped bytes.Buffer
	compressor := gzip.NewWriter(&zipped)
	if _, err = compressor.Write(encode(value)); err != nil {
		return nil, err
	}
	if err = compressor.Close(); err != nil {
		return nil, err
	}
	payload, payloadSignature, err := m.seal(document{"messageid": m.messageID, "sequencenumber": 1, "endofmsg": true, "compressionalgo": "GZIP", "data": b64(zipped.Bytes())})
	if err != nil {
		return nil, err
	}
	first := document{"mastertoken": m.master, "headerdata": header, "signature": signature}
	last := document{"payload": payload, "signature": payloadSignature}
	chunks, err := m.post(ctx, endpoint, append(encode(first), encode(last)...))
	if err != nil {
		return nil, err
	}
	var content bytes.Buffer
	for _, chunk := range chunks {
		if text(chunk["payload"]) == "" {
			continue
		}
		fragment, err := m.open(text(chunk["payload"]), text(chunk["signature"]))
		if err != nil {
			return nil, err
		}
		data, err := unb64(text(fragment["data"]))
		if err != nil {
			return nil, err
		}
		if text(fragment["compressionalgo"]) == "GZIP" {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			data, err = io.ReadAll(io.LimitReader(reader, 48<<20))
			reader.Close()
			if err != nil {
				return nil, err
			}
		}
		content.Write(data)
		if content.Len() > 48<<20 {
			return nil, errors.New("respuesta MSL demasiado grande")
		}
	}
	var result any
	if json.Unmarshal(content.Bytes(), &result) != nil {
		return nil, errors.New("respuesta MSL sin documento válido")
	}
	if root := mslObject(result); root != nil {
		if detail := root["error"]; detail != nil {
			return nil, serviceFailure(detail)
		}
		if nested, ok := root["result"]; ok {
			return nested, nil
		}
	}
	return result, nil
}

func serviceFailure(value any) error {
	if fields := mslObject(value); fields != nil {
		for _, name := range []string{"display", "message", "detail", "errorDisplayMessage"} {
			if description := text(fields[name]); description != "" {
				return fmt.Errorf("Netflix: %s", description)
			}
		}
		return fmt.Errorf("Netflix rechazó la solicitud (código %v)", fields["code"])
	}
	return errors.New("Netflix rechazó la solicitud")
}
