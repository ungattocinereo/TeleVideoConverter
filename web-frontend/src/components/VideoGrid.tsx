import { Video } from '@/types'
import { VideoCard } from './VideoCard'

interface VideoGridProps {
  videos: Video[]
  onDownload: (video: Video) => void
  onDelete: (video: Video) => void
  onVideoClick: (video: Video) => void
}

export function VideoGrid({ videos, onDownload, onDelete, onVideoClick }: VideoGridProps) {
  if (videos.length === 0) {
    return (
      <div className="text-center py-16">
        <p className="text-muted-foreground text-lg">No videos found</p>
        <p className="text-muted-foreground text-sm mt-2">
          Send a video URL to the Telegram bot to get started
        </p>
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-6">
      {videos.map((video) => (
        <VideoCard
          key={video.id}
          video={video}
          onDownload={onDownload}
          onDelete={onDelete}
          onClick={onVideoClick}
        />
      ))}
    </div>
  )
}
