package channel

import (
	"fmt"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type countModel struct{ count int }

func (m *countModel) Init() tea.Cmd { return nil }
func (m *countModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(string); ok {
		m.count++
	}
	return m, nil
}
func (m *countModel) View() tea.View { return tea.NewView("") }

func TestProgramSendContention(t *testing.T) {
	p := tea.NewProgram(&countModel{}, tea.WithoutRenderer(), tea.WithoutSignals(), tea.WithInput(nil))
	go func() { _, _ = p.Run() }()
	time.Sleep(50 * time.Millisecond)

	const flooders, perFlooder = 10, 100
	var wg sync.WaitGroup
	var keyBlock time.Duration

	for i := 0; i < flooders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perFlooder; j++ {
				p.Send(fmt.Sprintf("p-%d-%d", id, j))
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		s := time.Now()
		p.Send("KEY")
		keyBlock = time.Since(s)
	}()
	wg.Wait()

	t.Logf("Key blocked: %v (flood: %dx%d)", keyBlock, flooders, perFlooder)
	if keyBlock > 2*time.Second {
		t.Errorf("too long: %v", keyBlock)
	}
	p.Quit()
}
