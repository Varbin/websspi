package websspi

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

type stubAPI struct {
	acquireStatus    SECURITY_STATUS // the status code that should be returned in simulated calls to AcquireCredentialsHandle
	acceptStatus     SECURITY_STATUS // the status code that should be returned in simulated calls to AcceptSecurityContext
	acceptNewCtx     *CtxtHandle     // the context handle to be returned in simulated calls to AcceptSecurityContext
	acceptOutBuf     *SecBuffer      // the output buffer to be returned in simulated calls to AcceptSecurityContext
	deleteStatus     SECURITY_STATUS // the status code that should be returned in simulated calls to DeleteSecurityContext
	deleteCalled     bool            // true if DeleteSecurityContext has been called
	queryStatus      SECURITY_STATUS // the status code that should be returned in simulated calls to QueryContextAttributes
	queryOutBuf      *byte           // the buffer to be returned in simulated calls to QueryContextAttributes
	freeBufferStatus SECURITY_STATUS // the status code that should be returned in simulated calls to FreeContextBuffer
	freeCredsStatus  SECURITY_STATUS // the status code that should be returned in simulated calls to FreeCredentialsHandle
	validToken       string          // value that will be asumed to be a valid token
}

func (s *stubAPI) AcquireCredentialsHandle(
	principal *uint16,
	_package *uint16,
	credentialUse uint32,
	logonId *LUID,
	authData *byte,
	getKeyFn uintptr,
	getKeyArgument uintptr,
	credHandle *CredHandle,
	expiry *syscall.Filetime,
) SECURITY_STATUS {
	if s.acquireStatus != SEC_E_OK {
		return s.acquireStatus
	}
	credHandle = &CredHandle{}
	expiry = &syscall.Filetime{}
	return SEC_E_OK
}

func (s *stubAPI) AcceptSecurityContext(
	credential *CredHandle,
	context *CtxtHandle,
	input *SecBufferDesc,
	contextReq uint32,
	targDataRep uint32,
	newContext *CtxtHandle,
	output *SecBufferDesc,
	contextAttr *uint32,
	expiry *syscall.Filetime,
) SECURITY_STATUS {
	if s.acceptNewCtx != nil {
		*newContext = *s.acceptNewCtx
	}
	if s.acceptOutBuf != nil {
		*output.Buffers = *s.acceptOutBuf
	}
	return s.acceptStatus
}

func (s *stubAPI) QueryContextAttributes(context *CtxtHandle, attribute uint32, buffer *byte) SECURITY_STATUS {
	if s.queryOutBuf != nil {
		if attribute == SECPKG_ATTR_NAMES {
			inNames := (*SecPkgContext_Names)(unsafe.Pointer(buffer))
			outNames := (*SecPkgContext_Names)(unsafe.Pointer(s.queryOutBuf))
			*inNames = *outNames
		}
	}
	return s.queryStatus
}

func (s *stubAPI) DeleteSecurityContext(context *CtxtHandle) SECURITY_STATUS {
	s.deleteCalled = true
	return s.deleteStatus
}

func (s *stubAPI) FreeContextBuffer(buffer *byte) SECURITY_STATUS {
	return s.freeBufferStatus
}

func (s *stubAPI) FreeCredentialsHandle(handle *CredHandle) SECURITY_STATUS {
	return s.freeCredsStatus
}

type stubContextStore struct {
	contextHandle interface{} // local storage for the last value set by SetHandle
	getError      error       // Error value that should be returned on calls to GetHandle
	setError      error       // Error value that should be returned on calls to SetHandle
}

func (s *stubContextStore) GetHandle(r *http.Request) (interface{}, error) {
	return s.contextHandle, s.getError
}

func (s *stubContextStore) SetHandle(r *http.Request, w http.ResponseWriter, contextHandle interface{}) error {
	s.contextHandle = contextHandle
	return s.setError
}

// newTestAuthenticator creates an Authenticator for use in tests.
func newTestAuthenticator(t *testing.T) *Authenticator {
	names, err := syscall.UTF16PtrFromString("testuser")
	if err != nil {
		panic(err)
	}
	config := Config{
		contextStore: &stubContextStore{},
		authAPI: &stubAPI{
			acquireStatus:    SEC_E_OK,
			acceptStatus:     SEC_E_OK,
			acceptNewCtx:     nil,
			acceptOutBuf:     nil,
			deleteStatus:     SEC_E_OK,
			queryStatus:      SEC_E_OK,
			queryOutBuf:      (*byte)(unsafe.Pointer(&SecPkgContext_Names{userName: names})),
			freeBufferStatus: SEC_E_OK,
			freeCredsStatus:  SEC_E_OK,
			validToken:       "a87421000492aa874209af8bc028",
		},
		KrbPrincipal: "service@test.local",
	}
	auth, err := New(&config)
	if err != nil {
		t.Errorf("could not create new authenticator: %s", err)
	}
	return auth
}

func TestConfigValidate_NoContextStore(t *testing.T) {
	config := NewConfig()
	config.contextStore = nil
	err := config.Validate()
	if err == nil {
		t.Errorf("Config.Validate() returned nil (no error) when contextStore was nil, wanted error")
	}
}

func TestConfigValidate_NoAuthAPI(t *testing.T) {
	config := NewConfig()
	config.authAPI = nil
	err := config.Validate()
	if err == nil {
		t.Errorf("Config.Validate() returned nil (no error) when authAPI was nil, wanted error")
	}
}

func TestConfigValidate_Complete(t *testing.T) {
	config := NewConfig()
	config.KrbPrincipal = "service@test.local"
	err := config.Validate()
	if err != nil {
		t.Errorf("Config.Validate() returned error for a valid config, wanted nil (no error)")
	}
}

func TestNewAuthenticator_InvalidConfig(t *testing.T) {
	_, err := New(&Config{})
	if err == nil {
		t.Errorf("New() returns nil (no error) when Config was not valid, wanted error")
	}
}

func TestNewAuthenticator_ErrorOnAcquire(t *testing.T) {
	config := Config{
		contextStore: &stubContextStore{},
		authAPI:      &stubAPI{acquireStatus: SEC_E_INSUFFICIENT_MEMORY},
		KrbPrincipal: "service@test.local",
	}
	_, err := New(&config)
	if err == nil {
		t.Errorf("New() returns nil (no error) when AcquireCredentialHandle fails, wanted error")
	}
}

func TestFree_ErrorOnFreeCredentials(t *testing.T) {
	config := Config{
		contextStore: &stubContextStore{},
		authAPI:      &stubAPI{freeCredsStatus: SEC_E_INVALID_HANDLE},
		KrbPrincipal: "service@test.local",
	}
	auth, err := New(&config)
	if err != nil {
		t.Fatalf("New() failed with valid config, error: %v", err)
	}
	err = auth.Free()
	if err == nil {
		t.Error("Free() returns nil (no error) when FreeCredentialsHandle fails, wanted error")
	}
}

func TestFree_DeleteContexts(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
	w := httptest.NewRecorder()
	ctx := CtxtHandle{42, 314}
	err := auth.SetCtxHandle(r, w, &ctx)
	if err != nil {
		t.Fatalf("SetCtxHandle() failed with error %s, wanted no error", err)
	}
	err = auth.Free()
	if err != nil {
		t.Fatalf("Free() failed with error %s, wanted no error", err)
	}
	if !auth.Config.authAPI.(*stubAPI).deleteCalled {
		t.Errorf("Free() did NOT call DeleteSecurityContext, wanted at least one call")
	}
	if len(auth.ctxList) > 0 {
		t.Errorf("Free() did not delete all contexts. Have %d contexts, wanted 0", len(auth.ctxList))
	}
}

func TestFree_ErrorOnDeleteContexts(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).deleteStatus = SEC_E_INTERNAL_ERROR
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
	w := httptest.NewRecorder()
	ctx := CtxtHandle{42, 314}
	err := auth.SetCtxHandle(r, w, &ctx)
	if err != nil {
		t.Fatalf("SetCtxHandle() failed with error %s, wanted no error", err)
	}
	err = auth.Free()
	if err == nil {
		t.Errorf("Free() returns no error when DeleteSecurityContext fails, wanted an error")
	}
}

func TestAcceptOrContinue_SetCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
	w := httptest.NewRecorder()

	wantCtx := &CtxtHandle{42, 314}
	err := auth.SetCtxHandle(r, w, wantCtx)
	if err != nil {
		t.Fatalf("SetCtxHandle() failed with error %s, wanted no error", err)
	}
	value := auth.Config.contextStore.(*stubContextStore).contextHandle
	gotCtx, ok := value.(*CtxtHandle)
	if !ok || *gotCtx != *wantCtx {
		t.Errorf("SetCtxHandle() did not save the context value to the store, got = %v, want = %v", gotCtx, wantCtx)
	}
}

func TestAcceptOrContinue_ClearCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
	w := httptest.NewRecorder()

	err := auth.SetCtxHandle(r, w, nil)
	if err != nil {
		t.Fatalf("SetCtxHandle() failed with error %s, wanted no error", err)
	}
	value := auth.Config.contextStore.(*stubContextStore).contextHandle
	gotCtx, ok := value.(*CtxtHandle)
	wantCtx := &CtxtHandle{0, 0}
	if !ok || *gotCtx != *wantCtx {
		t.Errorf("SetCtxHandle() did not clear context value in store, got = %v, want = %v", gotCtx, wantCtx)
	}
}

func TestAcceptOrContinue_GetCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)

	wantCtx := &CtxtHandle{42, 314}
	auth.Config.contextStore.(*stubContextStore).contextHandle = wantCtx
	gotCtx, err := auth.GetCtxHandle(r)
	if err != nil {
		t.Fatalf("GetCtxHandle() failed with error %s, wanted no error", err)
	}
	if gotCtx == nil {
		t.Fatalf("GetCtxHandle() returned nil context, wanted %v", *wantCtx)
	}
	if *gotCtx != *wantCtx {
		t.Errorf("GetCtxHandle() returned wrong context handle, got = %v, want = %v", gotCtx, wantCtx)
	}
}

func TestAcceptOrContinue_GetEmptyCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://localhost:9000/", nil)

	wantCtx := &CtxtHandle{0, 0}
	auth.Config.contextStore.(*stubContextStore).contextHandle = wantCtx
	gotCtx, err := auth.GetCtxHandle(r)
	if err != nil {
		t.Fatalf("GetCtxHandle() failed with error %s, wanted no error", err)
	}
	if gotCtx != nil {
		t.Errorf("GetCtxHandle() returned %v for empty context handle, wanted nil", *gotCtx)
	}
}

func TestAcceptOrContinue_WithEmptyInput(t *testing.T) {
	auth := newTestAuthenticator(t)
	_, _, _, _, err := auth.AcceptOrContinue(nil, nil)
	// AcceptOrContinue should not panic on nil arguments
	if err == nil {
		t.Error("AcceptOrContinue(nil, nil) returned no error, should have returned an error")
	}
}

func TestAcceptOrContinue_WithOutputBuffer(t *testing.T) {
	wantData := [5]byte{2, 4, 8, 16, 32}
	buf := SecBuffer{uint32(len(wantData)), SECBUFFER_TOKEN, &wantData[0]}
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptOutBuf = &buf
	_, gotOut, _, _, _ := auth.AcceptOrContinue(nil, []byte{0})
	if gotOut == nil {
		t.Fatalf("AcceptOrContinue() returned no output data, wanted %v", wantData)
	}
	if !bytes.Equal(gotOut, wantData[:]) {
		t.Errorf("AcceptOrContinue() got %v for output data, wanted %v", gotOut, wantData)
	}
}

func TestAcceptOrContinue_ErrorOnFreeBuffer(t *testing.T) {
	data := [1]byte{0}
	buf := SecBuffer{uint32(len(data)), SECBUFFER_TOKEN, &data[0]}
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptOutBuf = &buf
	auth.Config.authAPI.(*stubAPI).freeBufferStatus = SEC_E_INVALID_HANDLE
	_, _, _, _, err := auth.AcceptOrContinue(nil, []byte{0})
	if err == nil {
		t.Error("AcceptOrContinue() returns no error when FreeContextBuffer fails, should have returned an error")
	}
}

func TestAcceptOrContinue_WithoutNewContext(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptNewCtx = &CtxtHandle{0, 0}
	newCtx, _, _, _, _ := auth.AcceptOrContinue(nil, []byte{0})
	if newCtx != nil {
		t.Error("AcceptOrContinue() returned a new context handle for a simulated call to AcceptSecurityContext that returns NULL")
	}
}

func TestAcceptOrContinue_WithNewContext(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptNewCtx = &CtxtHandle{42, 314}
	gotNewCtx, _, _, _, _ := auth.AcceptOrContinue(nil, []byte{0})
	if gotNewCtx == nil {
		t.Fatal("AcceptOrContinue() returned nil for new context handle for a simulated call to AcceptSecurityContext that returns a valid handle")
	}
	wantNewCtx := &CtxtHandle{42, 314}
	if *gotNewCtx != *wantNewCtx {
		t.Errorf("AcceptOrContinue() got new context handle = %v, want %v (returned by AcceptSecurityContext)", *gotNewCtx, *wantNewCtx)
	}
}

func TestAcceptOrContinue_OnErrorStatus(t *testing.T) {
	auth := newTestAuthenticator(t)
	tests := []struct {
		name        string
		errorStatus SECURITY_STATUS
	}{
		{"SEC_E_INCOMPLETE_MESSAGE", SEC_E_INCOMPLETE_MESSAGE},
		{"SEC_E_INSUFFICIENT_MEMORY", SEC_E_INSUFFICIENT_MEMORY},
		{"SEC_E_INTERNAL_ERROR", SEC_E_INTERNAL_ERROR},
		{"SEC_E_INVALID_HANDLE", SEC_E_INVALID_HANDLE},
		{"SEC_E_INVALID_TOKEN", SEC_E_INVALID_TOKEN},
		{"SEC_E_LOGON_DENIED", SEC_E_LOGON_DENIED},
		{"SEC_E_NOT_OWNER", SEC_E_NOT_OWNER},
		{"SEC_E_NO_AUTHENTICATING_AUTHORITY", SEC_E_NO_AUTHENTICATING_AUTHORITY},
		{"SEC_E_NO_CREDENTIALS", SEC_E_NO_CREDENTIALS},
		{"SEC_E_SECPKG_NOT_FOUND", SEC_E_SECPKG_NOT_FOUND},
		{"SEC_E_UNKNOWN_CREDENTIALS", SEC_E_UNKNOWN_CREDENTIALS},
		{"SEC_E_UNSUPPORTED_FUNCTION", SEC_E_UNSUPPORTED_FUNCTION},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth.Config.authAPI.(*stubAPI).acceptStatus = tt.errorStatus
			_, _, _, _, err := auth.AcceptOrContinue(nil, []byte{0})
			if err == nil {
				t.Errorf("AcceptOrContinue() returns no error when AcceptSecurityContext fails with %s", tt.name)
			}
		})
	}
}

func TestGetUsername_ErrorOnQueryAttributes(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).queryStatus = SEC_E_INTERNAL_ERROR
	_, err := auth.GetUsername(&CtxtHandle{0, 0})
	if err == nil {
		t.Errorf("GetUsername() returns no error when QueryContextAttributes fails, wanted an error")
	}
}

func TestGetUsername_ErrorOnFreeBuffer(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).freeBufferStatus = SEC_E_INTERNAL_ERROR
	_, err := auth.GetUsername(&CtxtHandle{0, 0})
	if err == nil {
		t.Errorf("GetUsername() returns no error when FreeContextBuffer fails, wanted an error")
	}
}

func TestGetUsername_Valid(t *testing.T) {
	auth := newTestAuthenticator(t)
	got, err := auth.GetUsername(&CtxtHandle{0, 0})
	if err != nil {
		t.Fatalf("GetUsername() failed with error %q, wanted no error", err)
	}
	if got != "testuser" {
		t.Errorf("GetUsername() got %s, want %s", got, "testuser")
	}
}

func TestReturn401_Headers(t *testing.T) {
	auth := newTestAuthenticator(t)
	w := httptest.NewRecorder()

	auth.Return401(w, "")

	got := w.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(got, "Negotiate") {
		t.Errorf("Return401() returned a WWW-Authenticate header that does not start with Negotiate, got = %q", got)
	}
}

func TestReturn401_WithOutputData(t *testing.T) {
	auth := newTestAuthenticator(t)
	w := httptest.NewRecorder()

	auth.Return401(w, "output-token")

	got := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(got, "output-token") {
		t.Errorf("The header returned by Return401() does not contain the output token, got = %q", got)
	}
}

func TestAuthenticate_NoAuthHeader(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)

	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returned nil (no error) for request without Authorization header, wanted an error")
	}
}

func TestAuthenticate_MultipleAuthHeaders(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Add("Authorization", "Negotiate a87421000492aa874209af8bc028")
	r.Header.Add("Authorization", "Negotiate a87421000492aa874209af8bc028")

	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returned nil (no error) for request with multiple Authorization headers, wanted an error")
	}
}

func TestAuthenticate_EmptyAuthHeader(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "")

	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returned nil (no error) for request with empty Authorization header, wanted an error")
	}
}

func TestAuthenticate_BadAuthPrefix(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "auth: neg")

	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returned nil (no error) for request with bad Authorization header, wanted an error")
	}
}

func TestAuthenticate_EmptyToken(t *testing.T) {
	auth := newTestAuthenticator(t)

	tests := []struct {
		name  string
		value string
	}{
		{"No space delimiter and no token", "Negotiate"},
		{"Space delimiter, but no token", "Negotiate "},
		{"Double space delimiter, but no token", "Negotiate  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://example.local/", nil)
			r.Header.Set("Authorization", tt.value)

			_, _, err := auth.Authenticate(r, nil)
			if err == nil {
				t.Errorf(
					"Authenticate() returned nil (no error) for request with bad Authorization header (%v), wanted an error",
					tt.name,
				)
			}
		})
	}
}

func TestAuthenticate_BadBase64(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a874-210004-92aa8742-09af8-bc028")

	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returned nil (no error) for request with token that is not valid base64 string, wanted an error")
	}
}

func TestAuthenticate_ErrorGetCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.contextStore.(*stubContextStore).getError = errors.New("internal error")
	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")
	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returns nil (no error) when GetCtxHandle fails, wanted an error")
	}
}

func TestAuthenticate_ErrorSetCtxHandle(t *testing.T) {
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptNewCtx = &CtxtHandle{42, 314}
	auth.Config.contextStore.(*stubContextStore).setError = errors.New("internal error")
	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")
	_, _, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Error("Authenticate() returns nil (no error) when SetCtxHandle fails, wanted an error")
	}
}

func TestAuthenticate_WithContinueAndOutputToken(t *testing.T) {
	wantData := [5]byte{2, 4, 8, 16, 32}
	buf := SecBuffer{uint32(len(wantData)), SECBUFFER_TOKEN, &wantData[0]}
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptStatus = SEC_I_CONTINUE_NEEDED
	auth.Config.authAPI.(*stubAPI).acceptOutBuf = &buf
	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")
	_, gotTokenB64, err := auth.Authenticate(r, nil)
	if err == nil {
		t.Fatal("Authenticate() returns nil (no error) on SEC_I_CONTINUE_NEEDED")
	}
	if !strings.Contains(err.Error(), "continue") {
		t.Errorf("Authenticate() returned wrong error value on SEC_I_CONTINUE_NEEDED, got = %q, want = %q", err, "Negotiation should continue")
	}
	if gotTokenB64 == "" {
		t.Error("Authenticate() returns no output token on SEC_I_CONTINUE_NEEDED")
	}
	wantTokenB64 := "AgQIECA="
	if gotTokenB64 != wantTokenB64 {
		t.Errorf("Authenticate() got output token = %q, want %q", gotTokenB64, wantTokenB64)
	}
}

func TestAuthenticate_OnErrorStatus(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")

	tests := []struct {
		name        string
		errorStatus SECURITY_STATUS
	}{
		{"SEC_E_INCOMPLETE_MESSAGE", SEC_E_INCOMPLETE_MESSAGE},
		{"SEC_E_INSUFFICIENT_MEMORY", SEC_E_INSUFFICIENT_MEMORY},
		{"SEC_E_INTERNAL_ERROR", SEC_E_INTERNAL_ERROR},
		{"SEC_E_INVALID_HANDLE", SEC_E_INVALID_HANDLE},
		{"SEC_E_INVALID_TOKEN", SEC_E_INVALID_TOKEN},
		{"SEC_E_LOGON_DENIED", SEC_E_LOGON_DENIED},
		{"SEC_E_NOT_OWNER", SEC_E_NOT_OWNER},
		{"SEC_E_NO_AUTHENTICATING_AUTHORITY", SEC_E_NO_AUTHENTICATING_AUTHORITY},
		{"SEC_E_NO_CREDENTIALS", SEC_E_NO_CREDENTIALS},
		{"SEC_E_SECPKG_NOT_FOUND", SEC_E_SECPKG_NOT_FOUND},
		{"SEC_E_UNKNOWN_CREDENTIALS", SEC_E_UNKNOWN_CREDENTIALS},
		{"SEC_E_UNSUPPORTED_FUNCTION", SEC_E_UNSUPPORTED_FUNCTION},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth.Config.authAPI.(*stubAPI).acceptStatus = tt.errorStatus
			_, _, err := auth.Authenticate(r, nil)
			if err == nil {
				t.Errorf("Authenticate() returns no error when AcceptSecurityContext fails with %s", tt.name)
			}
		})
	}
}

func TestAuthenticate_ValidBase64(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")

	_, _, err := auth.Authenticate(r, nil)
	if err != nil {
		t.Errorf(
			"Authenticate() returned error %q for request with valid base64 string, wanted nil (no error)",
			err,
		)
	}
}

func TestAuthenticate_ValidToken(t *testing.T) {
	auth := newTestAuthenticator(t)

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")

	_, _, err := auth.Authenticate(r, nil)
	if err != nil {
		t.Errorf(
			"Authenticate() with valid token returned error %q, wanted nil (no error)",
			err,
		)
	}
}

func TestAuthenticate_ReturnOutputOnSecEOK(t *testing.T) {
	data := [1]byte{0}
	buf := SecBuffer{uint32(len(data)), SECBUFFER_TOKEN, &data[0]}
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptStatus = SEC_E_OK
	auth.Config.authAPI.(*stubAPI).acceptOutBuf = &buf

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")

	want := "AA=="
	_, output, _ := auth.Authenticate(r, nil)
	if output == "" {
		t.Errorf("Authenticate() returns empty output token when AcceptSecurityContext returns SEC_E_OK with output buffer, wanted %q", want)
	}
}

func TestWithAuth_ValidToken(t *testing.T) {
	data := [1]byte{0}
	buf := SecBuffer{uint32(len(data)), SECBUFFER_TOKEN, &data[0]}
	auth := newTestAuthenticator(t)
	auth.Config.authAPI.(*stubAPI).acceptOutBuf = &buf

	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")
	w := httptest.NewRecorder()

	handlerCalled := false
	gotUsername := ""
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		info := r.Context().Value("UserInfo")
		userInfo, ok := info.(*UserInfo)
		if ok && userInfo != nil {
			gotUsername = userInfo.Username
		}
	})
	protectedHandler := auth.WithAuth(handler)
	protectedHandler.ServeHTTP(w, r)

	_ = auth.Free()

	code := w.Result().StatusCode
	if code != http.StatusOK {
		t.Errorf(
			"Got status %v for request with valid token, wanted StatusOK (%v)",
			code,
			http.StatusOK,
		)
	}

	if code != http.StatusOK && handlerCalled {
		t.Error("Handler was called, when status code was not OK. Handler should not be called for error status codes.")
	} else if code == http.StatusOK && !handlerCalled {
		t.Error("Handler was not called, even though token was valid")
	}

	wantUsername := "testuser"
	if gotUsername != wantUsername {
		t.Errorf("Username stored in request context is %q, want %q", gotUsername, wantUsername)
	}

	wantHeader := "AA=="
	gotHeader := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(gotHeader, wantHeader) {
		t.Errorf("WithAuth() does not return output token when AcceptSecurityContext returns SEC_E_OK with output buffer, wanted token %q", wantHeader)
	}
}

func TestWithAuth_OnErrorStatus(t *testing.T) {
	auth := newTestAuthenticator(t)
	r := httptest.NewRequest("GET", "http://example.local/", nil)
	r.Header.Set("Authorization", "Negotiate a87421000492aa874209af8bc028")

	tests := []struct {
		name        string
		errorStatus SECURITY_STATUS
	}{
		{"SEC_E_INCOMPLETE_MESSAGE", SEC_E_INCOMPLETE_MESSAGE},
		{"SEC_E_INSUFFICIENT_MEMORY", SEC_E_INSUFFICIENT_MEMORY},
		{"SEC_E_INTERNAL_ERROR", SEC_E_INTERNAL_ERROR},
		{"SEC_E_INVALID_HANDLE", SEC_E_INVALID_HANDLE},
		{"SEC_E_INVALID_TOKEN", SEC_E_INVALID_TOKEN},
		{"SEC_E_LOGON_DENIED", SEC_E_LOGON_DENIED},
		{"SEC_E_NOT_OWNER", SEC_E_NOT_OWNER},
		{"SEC_E_NO_AUTHENTICATING_AUTHORITY", SEC_E_NO_AUTHENTICATING_AUTHORITY},
		{"SEC_E_NO_CREDENTIALS", SEC_E_NO_CREDENTIALS},
		{"SEC_E_SECPKG_NOT_FOUND", SEC_E_SECPKG_NOT_FOUND},
		{"SEC_E_UNKNOWN_CREDENTIALS", SEC_E_UNKNOWN_CREDENTIALS},
		{"SEC_E_UNSUPPORTED_FUNCTION", SEC_E_UNSUPPORTED_FUNCTION},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth.Config.authAPI.(*stubAPI).acceptStatus = tt.errorStatus
			w := httptest.NewRecorder()

			handlerCalled := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
			})
			protectedHandler := auth.WithAuth(handler)
			protectedHandler.ServeHTTP(w, r)

			_ = auth.Free()

			code := w.Result().StatusCode
			if code != http.StatusUnauthorized {
				t.Errorf(
					"Got HTTP status %v for unauthorized request (when AcceptSecurityContext = 0x%x), wanted http.StatusUnauthorized (%v)",
					code,
					tt.errorStatus,
					http.StatusUnauthorized,
				)
			}

			if code == http.StatusUnauthorized && handlerCalled {
				t.Error("Handler was called, when status code was StatusUnauthorized. Handler should not be called for error status codes.")
			}
		})
	}
}
