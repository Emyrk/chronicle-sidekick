#import <Cocoa/Cocoa.h>
#import <Sparkle/Sparkle.h>

static SPUStandardUpdaterController *sidekickUpdaterController;

void SidekickStartUpdater(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (sidekickUpdaterController == nil) {
            sidekickUpdaterController = [[SPUStandardUpdaterController alloc]
                initWithStartingUpdater:YES
                updaterDelegate:nil
                userDriverDelegate:nil];
        }
    });
}

void SidekickCheckForUpdates(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (sidekickUpdaterController == nil) {
            sidekickUpdaterController = [[SPUStandardUpdaterController alloc]
                initWithStartingUpdater:YES
                updaterDelegate:nil
                userDriverDelegate:nil];
        }
        [sidekickUpdaterController checkForUpdates:nil];
    });
}
