package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

//go:embed data/*
var gameFiles embed.FS

type Element struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type GameState struct {
	Elements   map[string]Element `json:"elements"`
	Recipes    map[string]string  `json:"recipes"`
	Discovered []string           `json:"discovered"`
	Impossible []string
}

type TelegramBot struct {
	bot        *tgbotapi.BotAPI
	gameStates map[int64]*GameState
	userStates map[int64]UserState
}

type UserState struct {
	waitingForFirstElement  bool
	waitingForSecondElement bool
	firstElement            string
}

type CombineRequest struct {
	ElementOne string `json:"element_one"`
	ElementTwo string `json:"element_two"`
}

type CombineResponse struct {
	Success bool   `json:"success"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func loadEmbeddedJSON(filename string, v any) error {
	data, err := gameFiles.ReadFile(filename)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func clearScreen() {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("cmd", "/c", "cls")
		cmd.Stdout = os.Stdout
		cmd.Run()
	default:
		fmt.Print("\033[H\033[2J")
	}
}

func getInput(prompt string, scanner *bufio.Scanner) string {
	fmt.Print(prompt)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

func printSlowly(text string, delay time.Duration) {
	for _, char := range text {
		fmt.Print(string(char))
		time.Sleep(delay)
	}
	fmt.Println()
}

func getConfigDir() (string, error) {
	var configDir string
	switch runtime.GOOS {
	case "windows":
		configDir = filepath.Join(os.Getenv("APPDATA"), "open-craft")
	default:
		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(userConfigDir, "open-craft")
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	return configDir, nil
}

func getProgressFilePath() (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "progress.json"), nil
}

func getTelegramUserProgressPath(userID int64) (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", err
	}

	telegramDir := filepath.Join(configDir, "telegram")
	if err := os.MkdirAll(telegramDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create telegram directory: %w", err)
	}

	return filepath.Join(telegramDir, fmt.Sprintf("%d.json", userID)), nil
}

func loadGameState(dev bool) (*GameState, error) {
	gameState := &GameState{
		Elements:   make(map[string]Element),
		Recipes:    make(map[string]string),
		Discovered: make([]string, 0),
		Impossible: make([]string, 0),
	}

	if dev {
		elementsData, err := os.ReadFile(filepath.Join("data/elements.json"))
		if err != nil {
			return nil, fmt.Errorf("failed to load elements: %w", err)
		}
		if err := json.Unmarshal(elementsData, &gameState.Elements); err != nil {
			return nil, fmt.Errorf("failed to parse elements: %w", err)
		}

		recipesData, err := os.ReadFile(filepath.Join("data/recipes.json"))
		if err != nil {
			return nil, fmt.Errorf("failed to load recipes: %w", err)
		}
		if err := json.Unmarshal(recipesData, &gameState.Recipes); err != nil {
			return nil, fmt.Errorf("failed to parse recipes: %w", err)
		}

		impossibleData, err := os.ReadFile(filepath.Join("data/impossible.json"))
		if err != nil {
			return nil, fmt.Errorf("failed to load impossible: %w", err)
		}
		if err := json.Unmarshal(impossibleData, &gameState.Impossible); err != nil {
			return nil, fmt.Errorf("failed to parse impossible: %w", err)
		}
	} else {
		if err := loadEmbeddedJSON("data/elements.json", &gameState.Elements); err != nil {
			return nil, fmt.Errorf("failed to load elements: %w", err)
		}

		if err := loadEmbeddedJSON("data/recipes.json", &gameState.Recipes); err != nil {
			return nil, fmt.Errorf("failed to load recipes: %w", err)
		}

		if err := loadEmbeddedJSON("data/impossible.json", &gameState.Impossible); err != nil {
			return nil, fmt.Errorf("failed to load impossible elements: %w", err)
		}
	}

	progressPath, err := getProgressFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(progressPath)
	if err == nil {
		json.Unmarshal(data, &gameState.Discovered)
	}

	if len(gameState.Discovered) == 0 {
		gameState.Discovered = []string{"water", "fire", "earth", "wind"}
		gameState.saveLocalProgress()
	}

	return gameState, nil
}

func (gs *GameState) saveLocalProgress() error {
	progressPath, err := getProgressFilePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(gs.Discovered, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(progressPath, data, 0644)
}

func (gs *GameState) saveTelegramProgress(userID int64) error {
	progressPath, err := getTelegramUserProgressPath(userID)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(gs.Discovered, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(progressPath, data, 0644)
}

func (gs *GameState) isDiscovered(element string) bool {
	return slices.Contains(gs.Discovered, element)
}

func (gs *GameState) addDiscovered(element string) {
	if !gs.isDiscovered(element) {
		gs.Discovered = append(gs.Discovered, element)
	}
}

func normalizeElementName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")
	return name
}

func (gs *GameState) combineElements(elem1, elem2 string) string {
	combo1 := elem1 + "+" + elem2
	combo2 := elem2 + "+" + elem1

	if result, exists := gs.Recipes[combo1]; exists {
		gs.addDiscovered(result)
		return result
	}

	if result, exists := gs.Recipes[combo2]; exists {
		gs.addDiscovered(result)
		return result
	}

	return ""
}

func (gs *GameState) isImpossible(combo string) bool {
	elements := strings.Split(combo, "+")
	reverse := elements[1] + "+" + elements[0]

	return slices.Contains(gs.Impossible, combo) ||
		slices.Contains(gs.Impossible, reverse)
}

func (gs *GameState) getUntriedCombos() []string {
	var combos []string
	allElements := make([]string, 0, len(gs.Elements))

	for elemName := range gs.Elements {
		allElements = append(allElements, elemName)
	}
	sort.Strings(allElements)

	for i, elem1 := range allElements {
		for j := i; j < len(allElements); j++ {
			elem2 := allElements[j]
			combo1 := elem1 + "+" + elem2
			combo2 := elem2 + "+" + elem1

			if _, exists1 := gs.Recipes[combo1]; exists1 {
				continue
			}
			if _, exists2 := gs.Recipes[combo2]; exists2 {
				continue
			}

			if gs.isImpossible(combo1) {
				continue
			}

			combos = append(combos, fmt.Sprintf("%s + %s",
				gs.Elements[elem1].Name,
				gs.Elements[elem2].Name))
		}
	}

	return combos
}

func NewTelegramBot(token string, gameState *GameState) (*TelegramBot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	return &TelegramBot{
		bot:        bot,
		gameStates: make(map[int64]*GameState),
		userStates: make(map[int64]UserState),
	}, nil
}

func (tb *TelegramBot) getUserGameState(userID int64) (*GameState, error) {
	if gameState, exists := tb.gameStates[userID]; exists {
		return gameState, nil
	}

	gameState := &GameState{
		Elements:   make(map[string]Element),
		Recipes:    make(map[string]string),
		Discovered: make([]string, 0),
	}

	if err := loadEmbeddedJSON("data/elements.json", &gameState.Elements); err != nil {
		return nil, fmt.Errorf("failed to load elements: %w", err)
	}

	if err := loadEmbeddedJSON("data/recipes.json", &gameState.Recipes); err != nil {
		return nil, fmt.Errorf("failed to load recipes: %w", err)
	}

	progressPath, err := getTelegramUserProgressPath(userID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(progressPath)
	if err == nil {
		json.Unmarshal(data, &gameState.Discovered)
	}

	if len(gameState.Discovered) == 0 {
		gameState.Discovered = []string{"water", "fire", "earth", "wind"}
		gameState.saveTelegramProgress(userID)
	}

	tb.gameStates[userID] = gameState
	return gameState, nil
}

func handleCombineAPI(gameState *GameState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		elem1 := normalizeElementName(r.URL.Query().Get("element-one"))
		elem2 := normalizeElementName(r.URL.Query().Get("element-two"))

		combo1 := elem1 + "+" + elem2
		combo2 := elem2 + "+" + elem1

		response := CombineResponse{}

		if result, exists := gameState.Recipes[combo1]; exists {
			response.Success = true
			response.Result = gameState.Elements[result].Name
		} else if result, exists := gameState.Recipes[combo2]; exists {
			response.Success = true
			response.Result = gameState.Elements[result].Name
		} else {
			response.Success = false
			response.Error = "These elements cannot be combined"
		}

		json.NewEncoder(w).Encode(response)
	}
}

func (tb *TelegramBot) sendMainMenu(chatID int64) {
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("🔮 Combine Elements"),
			tgbotapi.NewKeyboardButton("📚 Discovered Elements"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("💡 Show Hints"),
			tgbotapi.NewKeyboardButton("📥 Download Save"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, "Choose an option:")
	msg.ReplyMarkup = keyboard
	tb.bot.Send(msg)
}

func (tb *TelegramBot) sendElementsList(chatID int64) {
	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	discovered := make([]string, len(gameState.Discovered))
	copy(discovered, gameState.Discovered)
	sort.Strings(discovered)

	var elements strings.Builder
	elements.WriteString("Available Elements:\n\n")
	for _, name := range discovered {
		elements.WriteString(fmt.Sprintf("- %s\n", gameState.Elements[name].Name))
	}
	elements.WriteString("\nEnter the first element:")

	msg := tgbotapi.NewMessage(chatID, elements.String())
	tb.bot.Send(msg)
}

func (tb *TelegramBot) sendDiscoveredElements(chatID int64) {
	keyboard := [][]tgbotapi.KeyboardButton{
		{
			tgbotapi.NewKeyboardButton("🌟 Primordial"),
			tgbotapi.NewKeyboardButton("🌿 Natural"),
		},
		{
			tgbotapi.NewKeyboardButton("⚗️ Chemical"),
			tgbotapi.NewKeyboardButton("🌪️ Atmospheric"),
		},
		{
			tgbotapi.NewKeyboardButton("✨ Celestial"),
			tgbotapi.NewKeyboardButton("🧬 Biological"),
		},
		{
			tgbotapi.NewKeyboardButton("⚡ Technological"),
			tgbotapi.NewKeyboardButton("🔮 Mythical"),
		},
		{
			tgbotapi.NewKeyboardButton("📋 Show All Discovered"),
		},
	}

	msg := tgbotapi.NewMessage(chatID, "Select a category to view discovered elements:")
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(keyboard...)
	tb.bot.Send(msg)
}

func (tb *TelegramBot) showElementsByCategory(chatID int64, category string) {
	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	var elements strings.Builder
	elements.WriteString(fmt.Sprintf("%s Elements:\n\n", category))

	discoveredInCategory := 0
	for _, name := range gameState.Discovered {
		if element, exists := gameState.Elements[name]; exists {
			if element.Category == category {
				elements.WriteString(fmt.Sprintf("- %s\n", element.Name))
				discoveredInCategory++
			}
		}
	}

	if discoveredInCategory == 0 {
		elements.WriteString("No elements discovered in this category yet!")
	}

	msg := tgbotapi.NewMessage(chatID, elements.String())
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("◀️ Back to Categories"),
			tgbotapi.NewKeyboardButton("🏠 Main Menu"),
		),
	)
	tb.bot.Send(msg)
}

func (tb *TelegramBot) sendHints(chatID int64) {
	hints := `Hints:
1. Try combining basic elements first
2. Some elements can be combined in multiple ways
3. Look for logical combinations (e.g., water + fire = steam)`

	msg := tgbotapi.NewMessage(chatID, hints)
	tb.bot.Send(msg)
}

func (tb *TelegramBot) sendSaveFile(chatID int64) {
	saveFilePath, err := getTelegramUserProgressPath(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error locating save file")
		tb.bot.Send(msg)
		return
	}

	if _, err := os.Stat(saveFilePath); os.IsNotExist(err) {
		msg := tgbotapi.NewMessage(chatID, "No save file found")
		tb.bot.Send(msg)
		return
	}

	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(saveFilePath))
	doc.Caption = fmt.Sprintf("Your save file containing %d discovered elements", len(gameState.Discovered))

	if _, err := tb.bot.Send(doc); err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error sending save file")
		tb.bot.Send(msg)
		return
	}
}

func (tb *TelegramBot) showAllDiscovered(chatID int64) {
	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	var elements strings.Builder
	elements.WriteString(fmt.Sprintf("All Discovered Elements (%d total):\n\n", len(gameState.Discovered)))

	discovered := make([]string, len(gameState.Discovered))
	copy(discovered, gameState.Discovered)
	sort.Strings(discovered)

	for _, name := range discovered {
		if element, exists := gameState.Elements[name]; exists {
			elements.WriteString(fmt.Sprintf("- %s (%s)\n", element.Name, element.Category))
		}
	}

	msg := tgbotapi.NewMessage(chatID, elements.String())
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("◀️ Back to Categories"),
			tgbotapi.NewKeyboardButton("🏠 Main Menu"),
		),
	)
	tb.bot.Send(msg)
}

func (tb *TelegramBot) handleFirstElement(chatID int64, element string) {
	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	element = normalizeElementName(element)
	if !gameState.isDiscovered(element) {
		msg := tgbotapi.NewMessage(chatID, "You haven't discovered this element yet! Try another one.")
		tb.bot.Send(msg)
		return
	}

	tb.userStates[chatID] = UserState{
		waitingForSecondElement: true,
		firstElement:            element,
	}

	msg := tgbotapi.NewMessage(chatID, "Enter the second element:")
	tb.bot.Send(msg)
}

func (tb *TelegramBot) handleSecondElement(chatID int64, firstElement, secondElement string) {
	gameState, err := tb.getUserGameState(chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Error loading game state")
		tb.bot.Send(msg)
		return
	}

	secondElement = normalizeElementName(secondElement)
	if !gameState.isDiscovered(secondElement) {
		msg := tgbotapi.NewMessage(chatID, "You haven't discovered this element yet! Try another one.")
		tb.bot.Send(msg)
		return
	}

	result := gameState.combineElements(firstElement, secondElement)
	if result != "" {
		gameState.saveTelegramProgress(chatID)
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("✨ You created: %s!", gameState.Elements[result].Name))
		tb.bot.Send(msg)
	} else {
		msg := tgbotapi.NewMessage(chatID, "❌ These elements cannot be combined.")
		tb.bot.Send(msg)
	}

	delete(tb.userStates, chatID)
	tb.sendMainMenu(chatID)
}

func (tb *TelegramBot) Start() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := tb.bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID
		msg := update.Message.Text

		switch msg {
		case "/start":
			welcomeMsg := tgbotapi.NewMessage(chatID, "Welcome to Open Craft! 🌟\nCombine elements to discover new ones!")
			tb.bot.Send(welcomeMsg)
			tb.sendMainMenu(chatID)
		case "🔮 Combine Elements":
			tb.userStates[chatID] = UserState{waitingForFirstElement: true}
			tb.sendElementsList(chatID)
		case "📚 Discovered Elements":
			tb.sendDiscoveredElements(chatID)
		case "💡 Show Hints":
			tb.sendHints(chatID)
		case "📥 Download Save":
			tb.sendSaveFile(chatID)
		case "🌟 Primordial":
			tb.showElementsByCategory(chatID, "Primordial")
		case "🌿 Natural":
			tb.showElementsByCategory(chatID, "Natural")
		case "⚗️ Chemical":
			tb.showElementsByCategory(chatID, "Chemical")
		case "🌪️ Atmospheric":
			tb.showElementsByCategory(chatID, "Atmospheric")
		case "✨ Celestial":
			tb.showElementsByCategory(chatID, "Celestial")
		case "🧬 Biological":
			tb.showElementsByCategory(chatID, "Biological")
		case "⚡ Technological":
			tb.showElementsByCategory(chatID, "Technological")
		case "🔮 Mythical":
			tb.showElementsByCategory(chatID, "Mythical")
		case "📋 Show All Discovered":
			tb.showAllDiscovered(chatID)
		case "◀️ Back to Categories":
			tb.sendDiscoveredElements(chatID)
		case "🏠 Main Menu":
			tb.sendMainMenu(chatID)
		default:
			if state, exists := tb.userStates[chatID]; exists {
				if state.waitingForFirstElement {
					tb.handleFirstElement(chatID, msg)
				} else if state.waitingForSecondElement {
					tb.handleSecondElement(chatID, state.firstElement, msg)
				}
			}
		}
	}
}

func main() {
	botToken := flag.String("bot", "", "Telegram bot token")
	devMode := flag.Bool("dev", false, "Enable developer mode")
	apiMode := flag.String("api", "", "Start API server on specified port (e.g. :8080)")
	flag.Parse()

	gameState, err := loadGameState(*devMode)
	if err != nil {
		fmt.Printf("Failed to load game state: %v\n", err)
		return
	}

	if *apiMode != "" {
		http.HandleFunc("/combine", handleCombineAPI(gameState))
		fmt.Printf("Starting API server on port %s...\n", *apiMode)
		if err := http.ListenAndServe(*apiMode, nil); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *botToken != "" {
		bot, err := NewTelegramBot(*botToken, gameState)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("Starting Telegram Bot...")
		bot.Start()
		return
	}

	scanner := bufio.NewScanner(os.Stdin)

	for {
		clearScreen()
		fmt.Println("\n🌟 === Open Craft === 🌟")
		fmt.Printf("\nDiscovered Elements: %d/%d\n", len(gameState.Discovered), len(gameState.Elements))

		fmt.Println("\n1. 🔮 Combine Elements")
		fmt.Println("2. 📚 View Discovered Elements")
		fmt.Println("3. 💡 Show Hints")
		fmt.Println("4. 💾 Save and Exit")
		if *devMode {
			fmt.Println("5. 🔍 View Untried Combinations (Dev)")
			fmt.Println("6. ⚡ Recipe Creator Flow (Dev)")
		}

		choice := getInput("\nChoose an option: ", scanner)

		switch choice {
		case "1":
			fmt.Println("\n=== Available Elements ===")

			discovered := make([]string, len(gameState.Discovered))
			copy(discovered, gameState.Discovered)
			sort.Strings(discovered)
			for _, name := range discovered {
				fmt.Printf("- %s\n", gameState.Elements[name].Name)
			}

			elem1 := normalizeElementName(getInput("\nFirst element: ", scanner))
			elem2 := normalizeElementName(getInput("Second element: ", scanner))

			if !gameState.isDiscovered(elem1) || !gameState.isDiscovered(elem2) {
				printSlowly("❌ You haven't discovered one or both elements yet!", 30*time.Millisecond)
				time.Sleep(2 * time.Second)
				continue
			}

			if result := gameState.combineElements(elem1, elem2); result != "" {
				printSlowly(fmt.Sprintf("✨ You created: %s!", gameState.Elements[result].Name), 30*time.Millisecond)
				gameState.saveLocalProgress()
			} else {
				printSlowly("❌ These elements cannot be combined.", 30*time.Millisecond)
			}
			time.Sleep(2 * time.Second)

		case "2":
			fmt.Println("\n=== Discovered Elements ===")

			discovered := make([]string, len(gameState.Discovered))
			copy(discovered, gameState.Discovered)
			sort.Strings(discovered)
			for _, name := range discovered {
				fmt.Printf("- %s\n", gameState.Elements[name].Name)
			}

			getInput("\nPress Enter to continue...", scanner)

		case "3":
			fmt.Println("\n=== Hints ===")
			fmt.Println("1. Try combining basic elements first")
			fmt.Println("2. Some elements can be combined in multiple ways")
			fmt.Println("3. Look for logical combinations (e.g., water + fire = steam)")
			getInput("\nPress Enter to continue...", scanner)

		case "4":
			if err := gameState.saveLocalProgress(); err != nil {
				fmt.Printf("Failed to save progress: %v\n", err)
			}
			printSlowly("Thanks for playing! Your progress has been saved.", 30*time.Millisecond)
			return

		case "5":
			if *devMode {
				fmt.Println("\n=== Untried Combinations ===")
				combos := gameState.getUntriedCombos()
				if len(combos) == 0 {
					fmt.Println("You've tried all possible combinations!")
				} else {
					fmt.Printf("\nFound %d untried combinations:\n\n", len(combos))
					for _, combo := range combos {
						fmt.Println(combo)
					}
				}
				getInput("\nPress Enter to continue...", scanner)
			} else {
				printSlowly("Invalid choice.", 30*time.Millisecond)
				time.Sleep(time.Second)
			}

		case "6":
			if *devMode {
				for {
					clearScreen()
					fmt.Println("\n=== Recipe Creator Flow ===")

					newGameState, err := loadGameState(true)
					if err != nil {
						fmt.Printf("Error reloading game state: %v\n", err)
						getInput("\nPress Enter to return to main menu...", scanner)
						break
					}

					gameState = newGameState
					combos := gameState.getUntriedCombos()
					remainingCount := len(combos)

					if remainingCount == 0 {
						fmt.Println("\nNo more combinations available to create recipes for!")
						getInput("\nPress Enter to return to main menu...", scanner)
						break
					}

					var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

					randomIndex := rng.Intn(len(combos))
					combo := combos[randomIndex]

					fmt.Printf("\nRemaining possible combinations: %d\n\n", remainingCount)
					fmt.Printf("Suggested combination to create recipe for:\n%s\n\n", combo)

					fmt.Println("Options:")
					fmt.Println("1. Next combination")
					fmt.Println("2. Return to main menu")

					subchoice := getInput("\nChoice: ", scanner)

					if subchoice != "1" {
						break
					}
				}
			}

		default:
			printSlowly("Invalid choice.", 30*time.Millisecond)
			time.Sleep(time.Second)
		}
	}
}
