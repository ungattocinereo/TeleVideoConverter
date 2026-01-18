import { HardDrive, FileVideo } from 'lucide-react'
import { Stats } from '@/types'

interface HeaderProps {
  stats: Stats | null
}

export function Header({ stats }: HeaderProps) {
  return (
    <header className="border-b bg-card">
      <div className="container mx-auto px-4 py-4 md:py-6">
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          {/* Logo and title */}
          <div className="flex items-center gap-3 min-w-0">
            <img
              src="/icon.png"
              alt="TeleVideo"
              className="h-8 w-8 md:h-10 md:w-10 rounded-lg flex-shrink-0"
            />
            <h1 className="text-xl md:text-2xl lg:text-3xl font-bold truncate">
              TeleVideo Converter
            </h1>
          </div>

          {/* Stats */}
          {stats && (
            <div className="flex gap-4 md:gap-6 text-xs md:text-sm flex-shrink-0">
              <div className="flex items-center gap-1.5 md:gap-2">
                <HardDrive className="h-3.5 w-3.5 md:h-4 md:w-4 text-muted-foreground" />
                <span className="text-muted-foreground whitespace-nowrap">
                  {stats.used_space_gb} / {stats.max_space_gb} GB
                </span>
              </div>
              <div className="flex items-center gap-1.5 md:gap-2">
                <FileVideo className="h-3.5 w-3.5 md:h-4 md:w-4 text-muted-foreground" />
                <span className="text-muted-foreground whitespace-nowrap">
                  {stats.total_videos} files
                </span>
              </div>
            </div>
          )}
        </div>
      </div>
    </header>
  )
}
