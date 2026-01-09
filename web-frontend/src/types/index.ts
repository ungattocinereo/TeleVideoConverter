export interface Video {
  id: number
  video_id: string
  telegram_user_id: number
  original_url: string
  title: string
  original_quality?: string
  downloaded_quality: string
  file_size: number
  processing_time: number
  format: string
  codec?: string
  source_platform?: string
  file_path: string
  thumbnail_path?: string
  thumbnail_url?: string
  download_timestamp: number
  delete_timestamp: number
  created_at: string
  time_remaining: string
}

export interface Stats {
  total_videos: number
  used_space_bytes: number
  used_space_gb: number
  max_space_gb: number
  free_space_gb: number
  downloads_7d: number
  downloads_30d: number
}

export interface WebSocketMessage {
  type: 'download_started' | 'download_progress' | 'download_complete' | 'video_deleted' | 'stats_update'
  data: any
}
