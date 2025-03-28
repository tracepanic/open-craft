package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type TestElement struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type Elements map[string]Element
type Recipes map[string]string

func TestDataValidation(t *testing.T) {
	dataDir := filepath.Join("data")

	elementsFile, err := os.ReadFile(filepath.Join(dataDir, "elements.json"))
	if err != nil {
		t.Fatalf("Failed to read elements.json: %v", err)
	}

	var elements Elements
	if err := json.Unmarshal(elementsFile, &elements); err != nil {
		t.Fatalf("Failed to parse elements.json: %v", err)
	}

	recipesFile, err := os.ReadFile(filepath.Join(dataDir, "recipes.json"))
	if err != nil {
		t.Fatalf("Failed to read recipes.json: %v", err)
	}

	var recipes Recipes
	if err := json.Unmarshal(recipesFile, &recipes); err != nil {
		t.Fatalf("Failed to parse recipes.json: %v", err)
	}

	t.Run("Check for duplicate element indexes", func(t *testing.T) {
		seen := make(map[string]bool)
		for index := range elements {
			lowIndex := strings.ToLower(index)
			if seen[lowIndex] {
				t.Errorf("Duplicate element index (case-insensitive): %s", index)
			}
			seen[lowIndex] = true
		}
	})

	t.Run("Check for duplicate element names", func(t *testing.T) {
		seen := make(map[string]bool)
		for _, elem := range elements {
			if seen[elem.Name] {
				t.Errorf("Duplicate element name: %s", elem.Name)
			}
			seen[elem.Name] = true
		}
	})

	t.Run("Check recipes", func(t *testing.T) {
		seenCombos := make(map[string]bool)

		for recipe, result := range recipes {
			parts := strings.Split(recipe, "+")
			if len(parts) != 2 {
				t.Errorf("Invalid recipe format: %s", recipe)
				continue
			}

			elem1, elem2 := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])

			if _, exists := elements[elem1]; !exists {
				t.Errorf("Recipe uses non-existent element: %s", elem1)
			}
			if _, exists := elements[elem2]; !exists {
				t.Errorf("Recipe uses non-existent element: %s", elem2)
			}

			if _, exists := elements[result]; !exists {
				t.Errorf("Recipe result is non-existent element: %s", result)
			}

			sortedCombo := []string{elem1, elem2}
			if elem1 > elem2 {
				sortedCombo[0], sortedCombo[1] = elem2, elem1
			}
			comboKey := strings.Join(sortedCombo, "+")

			if seenCombos[comboKey] {
				t.Errorf("Duplicate recipe combination: %s", recipe)
			}
			seenCombos[comboKey] = true
		}
	})
}
