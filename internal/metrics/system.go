package metrics

import (
	"math/rand"
	"runtime"
)

type SystemMetrics struct {
	CPUUsage    float64
	MemoryUsage float64
	QueueSize   int
}

// GetSystemMetrics returns current system metrics
// This is a simplified version for demonstration
func GetSystemMetrics(queueSize int) SystemMetrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Simple CPU usage simulation (in real implementation you'd use proper CPU monitoring)
	cpuUsage := 20.0 + rand.Float64()*30.0 // Random between 20-50%

	// Memory usage as percentage of allocated memory
	memoryUsage := float64(m.Alloc) / (1024 * 1024 * 100) * 100 // As percentage of 100MB baseline

	// Cap memory usage at reasonable level
	if memoryUsage > 90.0 {
		memoryUsage = 90.0
	}
	if memoryUsage < 10.0 {
		memoryUsage = 10.0 + rand.Float64()*20.0
	}

	return SystemMetrics{
		CPUUsage:    cpuUsage,
		MemoryUsage: memoryUsage,
		QueueSize:   queueSize,
	}
}
