import { useEffect, useRef, useCallback } from 'react'
import { WebSocketMessage } from '@/types'

export function useWebSocket(onMessage: (message: WebSocketMessage) => void) {
  const ws = useRef<WebSocket | null>(null)
  const reconnectTimeout = useRef<NodeJS.Timeout | null>(null)

  const connect = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const wsUrl = `${protocol}://${window.location.host}`
    ws.current = new WebSocket(`${wsUrl}/ws`)

    ws.current.onopen = () => {
      console.log('WebSocket connected')
    }

    ws.current.onmessage = (event) => {
      try {
        const message: WebSocketMessage = JSON.parse(event.data)
        onMessage(message)
      } catch (error) {
        console.error('Error parsing WebSocket message:', error)
      }
    }

    ws.current.onclose = () => {
      console.log('WebSocket disconnected. Reconnecting in 5 seconds...')
      reconnectTimeout.current = setTimeout(connect, 5000)
    }

    ws.current.onerror = (error) => {
      console.error('WebSocket error:', error)
    }
  }, [onMessage])

  useEffect(() => {
    connect()

    return () => {
      if (reconnectTimeout.current) {
        clearTimeout(reconnectTimeout.current)
      }
      if (ws.current) {
        ws.current.close()
      }
    }
  }, [connect])
}
