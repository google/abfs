// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package root

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

func AskYesNo(ctx context.Context, s string) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		fmt.Print(s)
		answer, err := ReadLineWithContext(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Println(err)
			}
			return false
		}
		switch strings.TrimSpace(answer) {
		case "y", "yes", "Y":
			return true
		case "", "no", "n", "N":
			return false
		default:
			// repeat
		}
	}
}

func ReadLineWithContext(ctx context.Context) (string, error) {
	resultChan := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			resultChan <- scanner.Text()
		} else {
			errChan <- scanner.Err()
		}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errChan:
		return "", err
	case result := <-resultChan:
		return result, nil
	}
}
