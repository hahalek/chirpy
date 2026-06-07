package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hahalek/chirpy/internal/database"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	godotenv.Load()
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		fmt.Printf(err.Error())
	}
	dbQueries := database.New(db)

	const filepathRoot = "."
	const port = "8080"

	mux := http.NewServeMux()
	apiCfg := apiConfig{
		requestsProcessed: atomic.Int32{},
		dbQ:               dbQueries,
		platform:          platform,
	}

	mux.Handle("/app/", apiCfg.middlewareMetricsInc(http.StripPrefix("/app", http.FileServer(http.Dir(filepathRoot)))))
	mux.HandleFunc("GET /api/healthz", healthzHandler)
	mux.HandleFunc("GET /admin/metrics", apiCfg.getMetrics)
	mux.HandleFunc("POST /admin/reset", apiCfg.resetUsers)
	mux.HandleFunc("POST /api/users", apiCfg.addUser)
	mux.HandleFunc("POST /api/chirps", apiCfg.postChirp)
	mux.HandleFunc("GET /api/chirps", apiCfg.getAllChirps)

	server := &http.Server{
		Handler: mux,
		Addr:    ":" + port,
	}

	log.Printf("Serving files from %s on port: %s\n", filepathRoot, port)
	log.Fatal(server.ListenAndServe())
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	UserID    uuid.UUID `json:"user_id"`
}

func healthzHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(http.StatusText(http.StatusOK)))
}

type apiConfig struct {
	requestsProcessed atomic.Int32
	dbQ               *database.Queries
	platform          string
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.requestsProcessed.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) getMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/html")
	w.Write([]byte(fmt.Sprintf(`<html>
  <body>
    <h1>Welcome, Chirpy Admin</h1>
    <p>Chirpy has been visited %d times!</p>
  </body>
</html>`, cfg.requestsProcessed.Load())))

}

func (cfg *apiConfig) resetUsers(w http.ResponseWriter, r *http.Request) {
	if cfg.platform != "dev" {
		w.Header().Add("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(http.StatusText(http.StatusForbidden)))
		return
	}
	cfg.dbQ.DeleteAllUsers(r.Context())
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(http.StatusText(http.StatusOK)))
}

func (cfg *apiConfig) addUser(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	type requestStructure struct {
		Email string `json:"email"`
	}
	reqParam := requestStructure{}
	decodeRequest(w, r, &reqParam)
	user, err := cfg.dbQ.CreateUser(r.Context(), reqParam.Email)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Something went wrong1")
		return
	}

	response := User{
		ID:        user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
		Email:     user.Email,
	}
	respondWithJSON(w, http.StatusCreated, response)
}

func (cfg *apiConfig) postChirp(w http.ResponseWriter, r *http.Request) {
	cleanedChirp, userID := validateChirp(w, r)

	createChirpParams := database.CreateChirpParams{
		Body:   cleanedChirp,
		UserID: userID,
	}

	newChirp, _ := cfg.dbQ.CreateChirp(r.Context(), createChirpParams)

	responseStructure := Chirp{
		ID:        newChirp.ID,
		CreatedAt: newChirp.CreatedAt,
		UpdatedAt: newChirp.UpdatedAt,
		Body:      newChirp.Body,
		UserID:    newChirp.UserID,
	}
	respondWithJSON(w, http.StatusCreated, responseStructure)
}

func validateChirp(w http.ResponseWriter, r *http.Request) (string, uuid.UUID) {
	defer r.Body.Close()
	type reqParameters struct {
		Body   string    `json:"body"`
		UserID uuid.UUID `json:"user_id"`
	}
	reqParam := reqParameters{}
	decodeRequest(w, r, &reqParam)
	if correct := checkChirtLength(w, reqParam.Body); correct == false {
		return "", uuid.UUID{}
	}
	cleanedBody := checkChirpProfanity(w, reqParam.Body)

	return cleanedBody, reqParam.UserID
}

func (cfg *apiConfig) getAllChirps(w http.ResponseWriter, r *http.Request) {
	chirps, err := cfg.dbQ.GetAllChirps(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong2")
	}
	chirpArray := []Chirp{}
	for _, chirp := range chirps {
		c := Chirp{
			ID:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserID:    chirp.UserID,
		}
		chirpArray = append(chirpArray, c)
	}
	respondWithJSON(w, http.StatusOK, chirpArray)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, reqParam any) {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(reqParam)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Something went wrong")
		return
	}
}

func checkChirtLength(w http.ResponseWriter, chirp string) bool {
	if len(chirp) > 140 {
		respondWithError(w, 400, "Chirp is too long")
		return false
	}
	return true
}

func checkChirpProfanity(w http.ResponseWriter, chirp string) string {
	profanities := map[string]bool{
		"kerfuffle": true,
		"sharbert":  true,
		"fornax":    true,
	}
	words := strings.Fields(chirp)
	cleanedWords := []string{}
	for _, word := range words {
		if profanities[strings.ToLower(word)] {
			word = "****"
		}
		cleanedWords = append(cleanedWords, word)
	}
	cleanedChirp := strings.Join(cleanedWords, " ")
	return cleanedChirp
}

func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) error {
	response, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(response)
	return nil
}

func respondWithError(w http.ResponseWriter, code int, msg string) error {
	return respondWithJSON(w, code, map[string]string{"error": msg})
}
