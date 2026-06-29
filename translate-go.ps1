# ainovel-cli Go source Việt hóa script
# Thay thế tiếng Trung bằng tiếng Việt hàng loạt

$map = @{
    # === User-facing strings (QUAN TRỌNG) ===
    '未定书名' = 'Chưa đặt tên sách'
    '正在运行' = 'Đang chạy'
    '准备就绪' = 'Sẵn sàng'
    '正在等待' = 'Đang chờ'
    '已完成' = 'Đã hoàn thành'
    '正在写入' = 'Đang ghi'
    '正在读取' = 'Đang đọc'
    '正在生成' = 'Đang tạo'
    '正在提交' = 'Đang nộp'
    '正在保存' = 'Đang lưu'
    '正在加载' = 'Đang tải'
    '正在初始化' = 'Đang khởi tạo'
    '正在导出' = 'Đang xuất'
    '正在导入' = 'Đang nhập'
    '正在分析' = 'Đang phân tích'
    '正在规划' = 'Đang规划'
    '正在审阅' = 'Đang审阅'
    '正在编辑' = 'Đang sửa'
    '正在检查' = 'Đang kiểm tra'
    
    # === Error messages ===
    '错误' = 'Lỗi'
    '警告' = 'Cảnh báo'
    '失败' = 'Thất bại'
    '无效的' = 'Không hợp lệ'
    '未找到' = 'Không tìm thấy'
    '无法' = 'Không thể'
    '已存在' = 'Đã tồn tại'
    '不支持' = 'Không hỗ trợ'
    '已取消' = 'Đã hủy'
    '已跳过' = 'Đã bỏ qua'
    '不可用' = 'Không可用'
    '不允许' = 'Không允 phép'
    '配置文件' = 'Tập tin cấu hình'
    '不在项目目录' = 'Không trong thư mục dự án'
    '模型列表' = 'Danh sách mô hình'
    '提供商' = 'Nhà cung cấp'
    '上下文窗口' = 'Cửa sổ ngữ cảnh'
    '未指定' = 'Chưa chỉ định'
    '缺少' = 'Thiếu'
    '格式错误' = 'Lỗi định dạng'
    '版本信息' = 'Thông tin phiên bản'
    '更新可用' = 'Có bản cập nhật'
    '已是最新版本' = 'Đã là phiên bản mới nhất'
    '版本检查失败' = 'Kiểm tra phiên bản thất bại'
    '更新失败' = 'Cập nhật thất bại'
    
    # === Status labels ===
    '运行中' = 'Đang chạy'
    '已完成' = 'Đã xong'
    '暂停' = 'Tạm dừng'
    '等待输入' = 'Chờ nhập'
    '等待用户' = 'Chờ người dùng'
    '等待确认' = 'Chờ xác nhận'
    
    # === TUI components ===
    '书名' = 'Tên sách'
    '章名' = 'Tên chương'
    '标题' = 'Tiêu đề'
    '章节' = 'Chương'
    '大纲' = 'Đại cương'
    '人物' = 'Nhân vật'
    '世界观' = 'Thế giới quan'
    '草稿' = 'Bản nháp'
    '正文' = 'Chính văn'
    '摘要' = 'Tóm tắt'
    '卷' = 'Quyển'
    '弧' = 'Vòng'
    '场景' = 'Cảnh'
    '伏笔' = '伏笔'
    '进度' = 'Tiến độ'
    '状态' = 'Trạng thái'
    '快照' = 'Chụp'
    '检查' = 'Kiểm tra'
    '提交' = 'Nộp'
    '审阅' = '审阅'
    '规划' = '规划'
    '写作' = 'Viết'
    '编辑' = 'Sửa'
    '读取' = 'Đọc'
    '保存' = 'Lưu'
    '创建' = 'Tạo'
    '导出' = 'Xuất'
    '导入' = 'Nhập'
    '恢复' = 'Phục hồi'
    '备份' = 'Sao lưu'
    '还原' = 'Hoàn nguyên'
    '模型' = 'Mô hình'
    '配置' = 'Cấu hình'
    '设置' = 'Thiết lập'
    '文件' = 'Tập tin'
    '目录' = 'Thư mục'
    '路径' = 'Đường dẫn'
    '版本' = 'Phiên bản'
    '密钥' = 'Khóa'
    '端点' = 'Endpoint'
    '令牌' = 'Token'
    '代理' = 'Proxy'
    
    # === Setup wizard ===
    '欢迎使用' = 'Chào mừng'
    '首次使用' = 'Lần đầu sử dụng'
    '请选择' = 'Vui lòng chọn'
    '请输入' = 'Vui lòng nhập'
    '按回车确认' = 'Nhấn Enter xác nhận'
    '按ESC取消' = 'Nhấn ESC hủy'
    '请选择模型提供商' = 'Vui lòng chọn nhà cung cấp mô hình'
    '请输入API密钥' = 'Vui lòng nhập khóa API'
    '请选择模型' = 'Vui lòng chọn mô hình'
    '确认配置' = 'Xác nhận cấu hình'
    '保存配置' = 'Lưu cấu hình'
    '测试连接' = 'Kiểm tra kết nối'
    '连接成功' = 'Kết nối thành công'
    '连接失败' = 'Kết nối thất bại'
    
    # === Novel workflow ===
    '新小说' = 'Tiểu thuyết mới'
    '继续写作' = 'Tiếp tục viết'
    '删减篇幅' = 'Giảm篇幅'
    '重写章节' = 'Viết lại chương'
    '润色章节' = 'Đánh bóng chương'
    '导入已有小说' = 'Nhập tiểu thuyết có sẵn'
    '风格模拟' = 'Mô phỏng phong cách'
    '查看统计' = 'Xem thống kê'
    '切换模型' = 'Đổi mô hình'
    '帮助' = 'Trợ giúp'
    '退出' = 'Thoát'
    '命令面板' = 'Bảng lệnh'
    
    # === Export formats ===
    '导出为' = 'Xuất dạng'
    '纯文本' = 'Văn bản thuần'
    '电子书' = 'Sách điện tử'
    '标记语言' = 'Ngôn ngữ đánh dấu'
    
    # === Review dimensions ===
    '一致性' = 'Nhất quán'
    '人物连贯' = 'Liên tục nhân vật'
    '节奏' = 'Nhịp'
    '叙事连贯' = 'Liên tục叙事'
    '伏笔健康' = 'Sức khỏe伏笔'
    '钩子质量' = 'Chất lượng móc'
    '审美质量' = 'Chất lượng thẩm mỹ'
    
    # === Commit params ===
    '章节摘要' = 'Tóm tắt chương'
    '关键事件' = 'Sự kiện trọng tâm'
    '时间线事件' = 'Sự kiện thời gian'
    '伏笔更新' = 'Cập nhật伏笔'
    '关系变化' = 'Thay đổi quan hệ'
    '状态变化' = 'Thay đổi trạng thái'
    '钩子类型' = 'Loại móc'
    '主导线索' = 'Line chủ đạo'
    '反馈' = 'Phản hồi'
    
    # === Common short words ===
    '是' = 'Đúng'
    '否' = 'Không'
    '无' = 'Không có'
    '空' = 'Rỗng'
    '全部' = 'Tất cả'
    '部分' = 'Phần'
    '开始' = 'Bắt đầu'
    '结束' = 'Kết thúc'
    '上一步' = 'Bước trước'
    '下一步' = 'Bước tiếp'
    '返回' = 'Quay lại'
    '默认' = 'Mặc định'
    '自定义' = 'Tuỳ chỉnh'
    '重置' = 'Đặt lại'
    '应用' = 'Áp dụng'
    '详细信息' = 'Chi tiết'
    '概览' = 'Tổng quan'
    '展开' = 'Mở rộng'
    '折叠' = 'Thu gọn'
    '启用' = 'Bật'
    '禁用' = 'Tắt'
    '完成' = 'Hoàn thành'
    '进行中' = 'Đang thực hiện'
    '待处理' = 'Chờ xử lý'
    '新' = 'Mới'
    '旧' = 'Cũ'
    '当前' = 'Hiện tại'
    '历史' = 'Lịch sử'
    '总计' = 'Tổng cộng'
    '剩余' = 'Còn lại'
    '未知' = 'Không rõ'
    '其他' = 'Khác'
}

$root = 'C:\Users\tunx8\.openclaw\workspace-main\ainovel-cli-fork'
$files = Get-ChildItem -Path $root -Recurse -Include *.go | Where-Object { 
    $content = Get-Content $_.FullName -Raw -Encoding UTF8
    $content -match '[\u4e00-\u9fff]'
}

$totalReplaced = 0
$totalFiles = 0

foreach ($file in $files) {
    $content = Get-Content $file.FullName -Raw -Encoding UTF8
    $original = $content
    $fileReplaced = 0
    
    foreach ($cn in $map.Keys) {
        $vi = $map[$cn]
        $count = ([regex]::Matches($content, [regex]::Escape($cn))).Count
        if ($count -gt 0) {
            $content = $content -replace [regex]::Escape($cn), $vi
            $fileReplaced += $count
        }
    }
    
    if ($content -ne $original) {
        Set-Content -Path $file.FullName -Value $content -Encoding UTF8 -NoNewline
        $totalReplaced += $fileReplaced
        $totalFiles++
        Write-Host "✓ $($file.FullName.Replace($root + '\', '')) - $fileReplaced thay thế"
    }
}

Write-Host "`nTổng: $totalReplaced thay thế trong $totalFiles files"
