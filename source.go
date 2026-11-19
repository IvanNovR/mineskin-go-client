package mineskin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"

	"github.com/IvanNovR/mineskin-go-client/internal/transport"
)

const (
	uploadFieldName   = "file"
	uploadFileName    = "skin.png"
	uploadContentType = "image/png"
	jsonContentType   = "application/json"

	// maxUploadBytes is the largest upload the client accepts (1 MiB).
	maxUploadBytes = 1 << 20
)

// newUploadTooLargeError is the error for an upload over maxUploadBytes.
func newUploadTooLargeError() error {
	return newInvalidInputError("skin upload exceeds the 1 MiB limit")
}

// readUpload reads r up to maxUploadBytes. It reads at most one byte more, so
// an endless stream is never buffered. It returns false when r holds more than
// maxUploadBytes.
func readUpload(r io.Reader) (data []byte, withinLimit bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, maxUploadBytes+1))
	if err != nil {
		return nil, false, err
	}
	return data, len(data) <= maxUploadBytes, nil
}

// Source is where a skin is generated from. Create one with FromURL, FromUser,
// FromReader, FromFile or FromImage. The interface is sealed.
type Source interface {
	requestBody(opts *GenerateOptions) (*transport.Body, error)
}

// GenerateOptions are the optional settings of a generate request. Zero values
// (including VariantAuto) are not sent. A nil *GenerateOptions is valid.
type GenerateOptions struct {
	// Name is the name of the skin; empty is not sent.
	Name string
	// Variant is the skin model. VariantAuto (the zero value) is not sent. Other
	// values are sent as they are and not validated client-side.
	Variant Variant
	// Visibility is who can see the skin. Empty is not sent. Values are sent as
	// they are and not validated client-side.
	Visibility Visibility
	// Cape is the UUID of a cape to apply; the zero UUID is not sent.
	Cape UUID
}

// formFields returns the set options as ordered name/value pairs: name,
// variant, visibility, cape. It is nil-safe.
func (o *GenerateOptions) formFields() [][2]string {
	if o == nil {
		return nil
	}
	var fields [][2]string
	if o.Name != "" {
		fields = append(fields, [2]string{"name", o.Name})
	}
	if o.Variant != VariantAuto {
		fields = append(fields, [2]string{"variant", string(o.Variant)})
	}
	if o.Visibility != "" {
		fields = append(fields, [2]string{"visibility", string(o.Visibility)})
	}
	if o.Cape != (UUID{}) {
		fields = append(fields, [2]string{"cape", o.Cape.String()})
	}
	return fields
}

// jsonBody is the JSON request payload; unset fields are omitted.
type jsonBody struct {
	URL        string     `json:"url,omitempty"`
	User       string     `json:"user,omitempty"`
	Name       string     `json:"name,omitempty"`
	Variant    Variant    `json:"variant,omitempty"`
	Visibility Visibility `json:"visibility,omitempty"`
	Cape       string     `json:"cape,omitempty"`
}

func newJSONBody(b jsonBody, opts *GenerateOptions) (*transport.Body, error) {
	if opts != nil {
		b.Name, b.Variant, b.Visibility = opts.Name, opts.Variant, opts.Visibility
		if opts.Cape != (UUID{}) {
			b.Cape = opts.Cape.String()
		}
	}
	data, err := json.Marshal(b)
	if err != nil {
		return nil, wrapClientError("encode request body", err)
	}
	return &transport.Body{ContentType: jsonContentType, Data: data}, nil
}

type urlSource struct{ url string }

// FromURL generates a skin from an image URL. The URL must be absolute with a
// scheme and a host; otherwise sending fails with ErrInvalidInput.
func FromURL(u string) Source { return urlSource{url: u} }

func (s urlSource) requestBody(opts *GenerateOptions) (*transport.Body, error) {
	parsed, err := url.Parse(s.url)
	if err != nil {
		return nil, newInvalidInputError("invalid skin URL: cannot be parsed")
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, newInvalidInputError("invalid skin URL: scheme and host are required")
	}
	return newJSONBody(jsonBody{URL: s.url}, opts)
}

type userSource struct{ id UUID }

// FromUser generates a skin from the skin of a player with the given UUID.
func FromUser(id UUID) Source { return userSource{id: id} }

func (s userSource) requestBody(opts *GenerateOptions) (*transport.Body, error) {
	return newJSONBody(jsonBody{User: s.id.String()}, opts)
}

type readerSource struct{ r io.Reader }

// FromReader uploads the PNG data read from r. The reader is read once and
// never closed by the client, so a FromReader Source is used up after one send
// (retrying or reusing it sends no data). The data is buffered in memory and
// limited to 1 MiB (a nil reader gives ErrInvalidInput, but a typed nil such
// as a nil *os.File wrapped in io.Reader is not detected and fails when read); more gives ErrInvalidInput and no request is sent. At most
// 1 MiB plus one byte is read from r.
func FromReader(r io.Reader) Source { return readerSource{r: r} }

func (s readerSource) requestBody(opts *GenerateOptions) (*transport.Body, error) {
	if s.r == nil {
		return nil, newInvalidInputError("reader source is nil")
	}
	data, ok, err := readUpload(s.r)
	if err != nil {
		return nil, wrapClientError("read skin upload", err)
	}
	if !ok {
		return nil, newUploadTooLargeError()
	}
	return newMultipartBody(data, opts)
}

type fileSource struct{ path string }

// FromFile uploads the file at path. The file is opened, read and closed on
// every send, so the Source can be reused. A file larger than 1 MiB gives
// ErrInvalidInput and no request is sent.
func FromFile(path string) Source { return fileSource{path: path} }

func (s fileSource) requestBody(opts *GenerateOptions) (*transport.Body, error) {
	if s.path == "" {
		return nil, newInvalidInputError("file path is empty")
	}
	data, ok, err := readFile(s.path)
	if err != nil {
		return nil, wrapClientError("read skin file", err)
	}
	if !ok {
		return nil, newUploadTooLargeError()
	}
	return newMultipartBody(data, opts)
}

// readFile opens, reads (up to maxUploadBytes) and closes the file at path.
func readFile(path string) ([]byte, bool, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path supplied by the caller by design
	if err != nil {
		return nil, false, err
	}
	data, ok, readErr := readUpload(f)
	closeErr := f.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	return data, ok, closeErr
}

type imageSource struct{ img image.Image }

// FromImage uploads img, PNG-encoded in memory. An encoded image larger than
// 1 MiB gives ErrInvalidInput and no request is sent.
func FromImage(img image.Image) Source { return imageSource{img: img} }

func (s imageSource) requestBody(opts *GenerateOptions) (*transport.Body, error) {
	if s.img == nil {
		return nil, newInvalidInputError("image is nil")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, s.img); err != nil {
		return nil, wrapClientError("encode skin image", err)
	}
	if buf.Len() > maxUploadBytes {
		return nil, newUploadTooLargeError()
	}
	return newMultipartBody(buf.Bytes(), opts)
}

// newMultipartBody builds an upload body: option text parts first, then the
// file part.
func newMultipartBody(data []byte, opts *GenerateOptions) (*transport.Body, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range opts.formFields() {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, wrapClientError("encode upload", err)
		}
	}
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {fmt.Sprintf(`form-data; name=%q; filename=%q`, uploadFieldName, uploadFileName)},
		"Content-Type":        {uploadContentType},
	})
	if err != nil {
		return nil, wrapClientError("encode upload", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, wrapClientError("encode upload", err)
	}
	if err := w.Close(); err != nil {
		return nil, wrapClientError("encode upload", err)
	}
	return &transport.Body{ContentType: w.FormDataContentType(), Data: buf.Bytes()}, nil
}
