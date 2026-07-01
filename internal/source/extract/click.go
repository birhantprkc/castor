package extract

import (
	"context"

	"github.com/chromedp/chromedp"
)

func click(ctx context.Context, x, y float64) error {
	return chromedp.Run(ctx, chromedp.MouseClickXY(x, y, chromedp.ButtonLeft))
}
