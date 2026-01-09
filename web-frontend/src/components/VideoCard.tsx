import { Download, Trash2, Film } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Video } from '@/types'
import { formatSize, formatDate } from '@/lib/utils'

interface VideoCardProps {
  video: Video
  onDownload: (video: Video) => void
  onDelete: (video: Video) => void
  onClick: (video: Video) => void
}

export function VideoCard({ video, onDownload, onDelete, onClick }: VideoCardProps) {
  const handleDownload = (e: React.MouseEvent) => {
    e.stopPropagation()
    onDownload(video)
  }

  const handleDelete = (e: React.MouseEvent) => {
    e.stopPropagation()
    onDelete(video)
  }

  return (
    <Card
      className="overflow-hidden hover:shadow-lg transition-shadow cursor-pointer group"
      onClick={() => onClick(video)}
    >
      <div className="aspect-video bg-muted relative overflow-hidden">
        {video.thumbnail_url ? (
          <img
            src={video.thumbnail_url}
            alt={video.title}
            className="w-full h-full object-cover"
          />
        ) : (
          <div className="flex items-center justify-center h-full">
            <Film className="h-16 w-16 text-muted-foreground" />
          </div>
        )}

        {/* Overlay buttons */}
        <div className="absolute inset-0 bg-black/60 opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center gap-2">
          <Button
            size="icon"
            variant="secondary"
            onClick={handleDownload}
            title="Download"
          >
            <Download className="h-4 w-4" />
          </Button>
          <Button
            size="icon"
            variant="destructive"
            onClick={handleDelete}
            title="Delete"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>

      <CardContent className="p-4">
        <h3 className="font-semibold text-sm mb-2 line-clamp-2" title={video.title}>
          {video.title}
        </h3>

        <div className="flex justify-between text-xs text-muted-foreground">
          <span>{formatSize(video.file_size)}</span>
          <span>{video.downloaded_quality}</span>
        </div>

        <div className="mt-2 text-xs text-muted-foreground">
          <div>🗑 Deletes in: {video.time_remaining}</div>
          <div className="mt-1">📅 {formatDate(video.download_timestamp)}</div>
        </div>
      </CardContent>
    </Card>
  )
}
