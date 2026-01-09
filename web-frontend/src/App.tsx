import { useState, useEffect } from 'react'
import { Header } from './components/Header'
import { VideoGrid } from './components/VideoGrid'
import { VideoModal } from './components/VideoModal'
import { UrlSubmissionForm } from './components/UrlSubmissionForm'
import { Input } from './components/ui/input'
import { Video, Stats, WebSocketMessage } from './types'
import { useWebSocket } from './hooks/useWebSocket'
import { Search } from 'lucide-react'

const API_URL = import.meta.env.VITE_API_URL || 'http://localhost:3001'

function App() {
  const [videos, setVideos] = useState<Video[]>([])
  const [filteredVideos, setFilteredVideos] = useState<Video[]>([])
  const [stats, setStats] = useState<Stats | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedVideo, setSelectedVideo] = useState<Video | null>(null)
  const [modalOpen, setModalOpen] = useState(false)

  // Fetch videos
  const fetchVideos = async () => {
    try {
      const response = await fetch(`${API_URL}/api/videos`)
      const data = await response.json()
      setVideos(data)
      setFilteredVideos(data)
    } catch (error) {
      console.error('Error fetching videos:', error)
    }
  }

  // Fetch stats
  const fetchStats = async () => {
    try {
      const response = await fetch(`${API_URL}/api/stats`)
      const data = await response.json()
      setStats(data)
    } catch (error) {
      console.error('Error fetching stats:', error)
    }
  }

  // WebSocket message handler
  const handleWebSocketMessage = (message: WebSocketMessage) => {
    console.log('WebSocket message:', message)

    switch (message.type) {
      case 'download_complete':
      case 'video_deleted':
      case 'stats_update':
        fetchVideos()
        fetchStats()
        break
    }
  }

  // Initialize WebSocket
  useWebSocket(handleWebSocketMessage)

  // Initial data fetch
  useEffect(() => {
    fetchVideos()
    fetchStats()

    // Refresh data every 30 seconds
    const interval = setInterval(() => {
      fetchVideos()
      fetchStats()
    }, 30000)

    return () => clearInterval(interval)
  }, [])

  // Filter videos based on search query
  useEffect(() => {
    if (searchQuery.trim() === '') {
      setFilteredVideos(videos)
    } else {
      const query = searchQuery.toLowerCase()
      const filtered = videos.filter((video) =>
        video.title.toLowerCase().includes(query)
      )
      setFilteredVideos(filtered)
    }
  }, [searchQuery, videos])

  // Download video
  const handleDownload = async (video: Video) => {
    try {
      const response = await fetch(`${API_URL}/api/videos/${video.video_id}/download`)
      const blob = await response.blob()
      const url = window.URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${video.title}.${video.format}`
      document.body.appendChild(a)
      a.click()
      window.URL.revokeObjectURL(url)
      document.body.removeChild(a)
    } catch (error) {
      console.error('Error downloading video:', error)
      alert('Error downloading video')
    }
  }

  // Delete video
  const handleDelete = async (video: Video) => {
    if (!confirm(`Are you sure you want to delete "${video.title}"?`)) {
      return
    }

    try {
      await fetch(`${API_URL}/api/videos/${video.video_id}`, {
        method: 'DELETE',
      })

      // Remove from local state
      setVideos(videos.filter((v) => v.id !== video.id))
      fetchStats()
    } catch (error) {
      console.error('Error deleting video:', error)
      alert('Error deleting video')
    }
  }

  // Open video modal
  const handleVideoClick = (video: Video) => {
    setSelectedVideo(video)
    setModalOpen(true)
  }

  // Submit URL for download
  const handleUrlSubmit = async (url: string, quality: string) => {
    try {
      const response = await fetch(`${API_URL}/api/submit-url`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ url, quality }),
      })

      if (!response.ok) {
        const error = await response.json()
        throw new Error(error.detail || 'Failed to submit URL')
      }

      // Refresh videos and stats after successful submission
      setTimeout(() => {
        fetchVideos()
        fetchStats()
      }, 2000)
    } catch (error) {
      console.error('Error submitting URL:', error)
      throw error
    }
  }

  return (
    <div className="min-h-screen bg-background">
      <Header stats={stats} />

      <main className="container mx-auto px-4 py-8">
        {/* URL Submission Form */}
        <div className="mb-8 max-w-2xl">
          <UrlSubmissionForm onSubmit={handleUrlSubmit} />
        </div>

        {/* Search bar */}
        <div className="mb-8 max-w-md">
          <div className="relative">
            <Search className="absolute left-3 top-1/2 transform -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              type="text"
              placeholder="Search videos..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-10"
            />
          </div>
        </div>

        {/* Video grid */}
        <VideoGrid
          videos={filteredVideos}
          onDownload={handleDownload}
          onDelete={handleDelete}
          onVideoClick={handleVideoClick}
        />
      </main>

      {/* Video details modal */}
      <VideoModal
        video={selectedVideo}
        open={modalOpen}
        onOpenChange={setModalOpen}
        onDownload={handleDownload}
        onDelete={handleDelete}
      />
    </div>
  )
}

export default App
