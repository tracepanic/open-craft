package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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
	Name string `json:"name"`
}

type GameState struct {
	Elements   map[string]Element `json:"elements"`
	Recipes    map[string]string  `json:"recipes"`
	Discovered []string           `json:"discovered"`
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

func loadGameState() (*GameState, error) {
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
	return strings.ToLower(strings.TrimSpace(name))
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

func (tb *TelegramBot) sendMainMenu(chatID int64) {
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("🔮 Combine Elements"),
			tgbotapi.NewKeyboardButton("📚 Discovered Elements"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("💡 Show Hints"),
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
	elements.WriteString("Discovered Elements:\n\n")
	for _, name := range discovered {
		elements.WriteString(fmt.Sprintf("- %s\n", gameState.Elements[name].Name))
	}

	msg := tgbotapi.NewMessage(chatID, elements.String())
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
	flag.Parse()

	gameState, err := loadGameState()
	if err != nil {
		fmt.Printf("Failed to load game state: %v\n", err)
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

		default:
			printSlowly("Invalid choice.", 30*time.Millisecond)
			time.Sleep(time.Second)
		}
	}
}
