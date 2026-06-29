// #C:\Code\PowerWork\power-iot-system\backend\internal\core\iot\mqtt_service.go
package iot

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"gorm.io/gorm"
	
	// ✅ 正確引用：使用新的 domain package
	"power-iot-backend/internal/core/domain"
)

// MqttPayload 定義硬體傳來的 JSON 格式
type MqttPayload struct {
	MacAddress string  `json:"mac"`
	Voltage    float64 `json:"v"`
	Current    float64 `json:"c"`
	Power      float64 `json:"p"`
	KwhTotal   float64 `json:"kwh"`
	Timestamp  int64   `json:"ts"` // Unix Timestamp
}

type MqttService struct {
	client mqtt.Client
	db     *gorm.DB
}

// NewMqttService 初始化 MQTT 客戶端
func NewMqttService(brokerURL string, db *gorm.DB) *MqttService {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID("go_backend_subscriber")
	opts.SetAutoReconnect(true)
	
	// 連線成功的回調
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		fmt.Println("📡 MQTT Broker 已連線！")
	})
	// 連線丟失的回調
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		fmt.Printf("⚠️ MQTT 連線中斷: %v\n", err)
	})

	client := mqtt.NewClient(opts)
	return &MqttService{
		client: client,
		db:     db,
	}
}

// Connect 建立連線
func (s *MqttService) Connect() error {
	if token := s.client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}
	return nil
}

// Subscribe 訂閱設備數據主題
func (s *MqttService) Subscribe() {
	// 假設硬體上傳的主題是 "device/upload/data"
	topic := "device/upload/data"
	token := s.client.Subscribe(topic, 0, s.handleMessage)
	token.Wait()
	fmt.Printf("🎧 已訂閱主題: %s\n", topic)
}

// handleMessage 處理接收到的訊息 (核心邏輯)
func (s *MqttService) handleMessage(client mqtt.Client, msg mqtt.Message) {
	payload := msg.Payload()
	// fmt.Printf("收到訊息: %s\n", payload) // Debug 用

	var data MqttPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		log.Printf("❌ JSON 解析失敗: %v", err)
		return
	}

	// 使用 Goroutine 非同步處理，避免阻塞 MQTT 接收線程
	go s.processData(data)
}

func (s *MqttService) processData(data MqttPayload) {
	// 1. 時間校正邏輯
	// 如果硬體時間 < 2020-01-01 (Timestamp: 1577836800)，視為異常 (NTP 失敗)
	// 強制使用 Server 當下時間
	recordTime := time.Unix(data.Timestamp, 0)
	if data.Timestamp < 1577836800 {
		fmt.Printf("⚠️ [Time Sync] 設備 %s 時間異常 (%v), 使用 Server Time 校正\n", data.MacAddress, recordTime)
		recordTime = time.Now()
	}

	// 2. 查找設備 (使用新的 domain.Device)
	var device domain.Device
	// Preload AlertSettings 是為了後續檢查警報用
	if err := s.db.Preload("AlertSettings").Where("mac_address = ?", data.MacAddress).First(&device).Error; err != nil {
		// 如果找不到設備，暫時忽略 (或是可以實作自動註冊邏輯)
		// log.Printf("⚠️ 收到未知設備數據: %s", data.MacAddress)
		return
	}

	// 3. 更新設備線上狀態
	s.db.Model(&device).Updates(map[string]interface{}{
		"is_online": true,
		"last_seen": recordTime,
	})

	// 4. 寫入電力數據 (PowerReading)
	reading := domain.PowerReading{
		Time:     recordTime,
		DeviceID: device.ID,
		Voltage:  data.Voltage,
		Current:  data.Current,
		Power:    data.Power,
		KwhTotal: data.KwhTotal,
	}
	if err := s.db.Create(&reading).Error; err != nil {
		log.Printf("❌ 寫入數據失敗: %v", err)
	}

	// 5. 檢查是否觸發警報
	s.checkAlerts(device, data, recordTime)
}

// checkAlerts 檢查警報規則
func (s *MqttService) checkAlerts(device domain.Device, data MqttPayload, t time.Time) {
	settings := device.AlertSettings
	
	// 如果沒有設定警報或警報未啟用，直接返回
	if settings.ID == 0 || !settings.IsEnabled {
		return
	}

	// 規則：非營業時間 (Curfew) 偵測
	if settings.NonUsageStartTime != "" && settings.NonUsageEndTime != "" {
		currentHM := t.Format("15:04") // 取得目前的 "時:分"
		inRange := false
		
		// 判斷時間區間 (需考慮跨午夜的情況，例如 22:00 到 06:00)
		if settings.NonUsageStartTime > settings.NonUsageEndTime {
			// 跨午夜 (e.g., 22:00 ~ 06:00)
			if currentHM >= settings.NonUsageStartTime || currentHM <= settings.NonUsageEndTime {
				inRange = true
			}
		} else {
			// 同一天 (e.g., 09:00 ~ 18:00)
			if currentHM >= settings.NonUsageStartTime && currentHM <= settings.NonUsageEndTime {
				inRange = true
			}
		}

		// 如果在非營業時間內，且功率 > 10W (避免待機誤判)，觸發警報
		if inRange && data.Power > 10.0 {
			alert := domain.AlertLog{
				DeviceID:  device.ID,
				Type:      "CURFEW_USAGE", // 警報類型代碼
				Message:   fmt.Sprintf("非營業時間異常運轉 (偵測功率: %.2f W)", data.Power),
				Voltage:   data.Voltage,
				Current:   data.Current,
				Power:     data.Power,
				CreatedAt: t,
				IsRead:    false,
			}
			
			// 寫入警報紀錄
			if err := s.db.Create(&alert).Error; err == nil {
				fmt.Printf("🚨 [Alert] 設備 %s 觸發非營業時間警報！\n", device.Name)
				// TODO: 這裡未來可以串接 FCM 推播或 Line Notify
			}
		}
	}
}